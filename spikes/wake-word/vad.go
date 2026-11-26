package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-linux"
)

const vadWindow = 512

type seg struct{ On, Off float64 }

func newVAD(thr float64) *sherpa.VoiceActivityDetector {
	cfg := sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model: "data/models/silero_vad.onnx", Threshold: float32(thr),
			MinSilenceDuration: 0.25, MinSpeechDuration: 0.1, WindowSize: vadWindow, MaxSpeechDuration: 30,
		},
		SampleRate: sampleRate, NumThreads: 1, Provider: "cpu",
	}
	v := sherpa.NewVoiceActivityDetector(&cfg, 30)
	if v == nil {
		must(fmt.Errorf("cannot create VAD"))
	}
	return v
}

// vadSegments streams x through Silero VAD and returns when IsSpeech turned on/off.
func vadSegments(v *sherpa.VoiceActivityDetector, x []float32) []seg {
	var out []seg
	on := false
	var cur seg
	for i := 0; i+vadWindow <= len(x); i += vadWindow {
		v.AcceptWaveform(x[i : i+vadWindow])
		t := float64(i+vadWindow) / sampleRate
		s := v.IsSpeech()
		if s && !on {
			cur = seg{On: t}
		}
		if !s && on {
			cur.Off = t
			out = append(out, cur)
		}
		on = s
		for !v.IsEmpty() {
			v.Pop()
		}
	}
	if on {
		cur.Off = float64(len(x)) / sampleRate
		out = append(out, cur)
	}
	return out
}

func cmdVAD(args []string) {
	fs := flag.NewFlagSet("vad", flag.ExitOnError)
	wav := fs.String("wav", "", "wav")
	thr := fs.Float64("thr", 0.5, "silero threshold")
	quiet := fs.Bool("q", false, "summary only")
	fs.Parse(args)
	x := readWav(*wav)
	v := newVAD(*thr)
	t0 := cpuSeconds()
	segs := vadSegments(v, x)
	cpu := cpuSeconds() - t0
	total := 0.0
	for _, s := range segs {
		total += s.Off - s.On
		if !*quiet {
			fmt.Printf("speech %8.2fs -> %8.2fs (%.2fs)\n", s.On, s.Off, s.Off-s.On)
		}
	}
	dur := float64(len(x)) / sampleRate
	fmt.Printf("%d segments, %.1f%% of %.1f s flagged as speech; CPU %.2f s = %.2f%% of one core (%.0fx realtime)\n",
		len(segs), 100*total/dur, dur, cpu, 100*cpu/dur, dur/cpu)
}

func cmdVADTest(args []string) {
	fs := flag.NewFlagSet("vadtest", flag.ExitOnError)
	index := fs.String("index", "data/pos/index.json", "index")
	thr := fs.Float64("thr", 0.5, "threshold")
	fs.Parse(args)
	clips, dataDir := loadIndex(*index)
	type agg struct {
		n, rec int
		lat    []float64
		pre    int
	}
	res := map[string]*agg{}
	v := newVAD(*thr)
	for _, c := range clips {
		x := readWav(filepath.Join(dataDir, c.File))
		v.Reset()
		segs := vadSegments(v, x)
		a := res[c.SNR]
		if a == nil {
			a = &agg{}
			res[c.SNR] = a
		}
		a.n++
		cover, firstOn, already := 0.0, math.Inf(1), false
		for _, s := range segs {
			lo, hi := math.Max(s.On, c.Onset), math.Min(s.Off, c.End)
			if hi > lo {
				cover += hi - lo
			}
			if s.On <= c.Onset && s.Off > c.Onset {
				already = true
			}
			if s.On > c.Onset && s.On < firstOn {
				firstOn = s.On
			}
		}
		if cover >= 0.5*(c.End-c.Onset) {
			a.rec++
		}
		if already {
			a.pre++
		} else if !math.IsInf(firstOn, 1) {
			a.lat = append(a.lat, firstOn-c.Onset)
		}
	}
	fmt.Printf("Silero VAD (thr %.2f, min silence 0.25 s) on synthetic keyword clips\n\n| condition | clips | utterance >=50%% covered | VAD already on at onset (bg speech/music) | onset delay ms median/p95 (otherwise) |\n|---|---|---|---|---|\n", *thr)
	for _, s := range snrOrder {
		a := res[s]
		if a == nil {
			continue
		}
		sort.Float64s(a.lat)
		med, p95 := math.NaN(), math.NaN()
		if len(a.lat) > 0 {
			med = a.lat[len(a.lat)/2] * 1000
			p95 = a.lat[int(math.Min(float64(len(a.lat)-1), math.Ceil(0.95*float64(len(a.lat)))-1))] * 1000
		}
		fmt.Printf("| %s | %d | %.0f%% | %.0f%% | %.0f / %.0f |\n", s, a.n, 100*float64(a.rec)/float64(a.n), 100*float64(a.pre)/float64(a.n), med, p95)
	}
}

// cmdListen reads raw little-endian float32 16 kHz mono PCM on stdin (ADR-0021
// frame format) and prints detections. Pipe a recorder into it:
//
//	parec --format=float32le --rate=16000 --channels=1 | wakeword listen
func cmdListen(args []string) {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	thr := fs.Float64("thr", 0.25, "wake threshold (lower = more sensitive)")
	boost := fs.Float64("boost", 1.0, "keyword boost score (higher = more sensitive)")
	s2thr := fs.Float64("s2thr", 0.25, "stage-2 threshold")
	s2boost := fs.Float64("s2boost", 1.0, "stage-2 boost")
	fs.Parse(args)
	p := NewPipeline(*thr, *boost, *s2thr, *s2boost, false)
	defer p.Close()
	fmt.Fprintf(os.Stderr, "listening (wake thr %.2f, boost %.1f). Say 'Alfred', 'Jarvis', 'Hey Footman', 'Alfred, stop everything'. Ctrl-C to stop.\n", *thr, *boost)
	buf := make([]byte, 4*sampleRate/10)
	start := time.Now()
	for {
		_, err := io.ReadFull(os.Stdin, buf)
		if err != nil {
			return
		}
		x := make([]float32, len(buf)/4)
		for i := range x {
			x[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:]))
		}
		for _, e := range p.Feed(x) {
			if e.Kind == "wake" || e.Kind == "bg" {
				fmt.Printf("%6.1fs  %-4s %s\n", time.Since(start).Seconds(), e.Kind, e.Name)
			}
		}
	}
}
