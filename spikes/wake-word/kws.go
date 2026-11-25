package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-linux"
)

const sampleRate = 16000

var modelDir = "data/models/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01"

// Event is one detection. T is the audio time (seconds) at which the engine
// reported it, i.e. the number of samples fed so far / 16000.
type Event struct {
	Kind string // wake | s2 (gated) | bg (break-glass fired) | raw (ungated stage 2, diagnostic)
	Name string
	T    float64
}

func newSpotter(tokensFile string, thr, boost float64) *sherpa.KeywordSpotter {
	raw, err := os.ReadFile(tokensFile)
	must(err)
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		toks, name, _ := strings.Cut(line, " @")
		fmt.Fprintf(&b, "%s :%g #%g @%s\n", toks, boost, thr, name)
	}
	cfg := sherpa.KeywordSpotterConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{
				Encoder: filepath.Join(modelDir, "encoder-epoch-12-avg-2-chunk-16-left-64.int8.onnx"),
				Decoder: filepath.Join(modelDir, "decoder-epoch-12-avg-2-chunk-16-left-64.onnx"),
				Joiner:  filepath.Join(modelDir, "joiner-epoch-12-avg-2-chunk-16-left-64.int8.onnx"),
			},
			Tokens:     filepath.Join(modelDir, "tokens.txt"),
			NumThreads: 1,
			Provider:   "cpu",
		},
		MaxActivePaths:    4,
		KeywordsBuf:       b.String(),
		KeywordsBufSize:   b.Len(),
		KeywordsScore:     float32(boost),
		KeywordsThreshold: float32(thr),
	}
	sp := sherpa.NewKeywordSpotter(&cfg)
	if sp == nil {
		must(fmt.Errorf("could not create keyword spotter from %s", tokensFile))
	}
	return sp
}

// Pipeline is the two-stage detector: a wake spotter always runs; a second
// "stop everything / cancel everything" spotter runs only for Window seconds
// after a break-glass-capable wake word (ADR-0017). Diag also runs the stage-2
// spotter ungated, only to show that the gate is what prevents bare phrases.
type Pipeline struct {
	wake, s2     *sherpa.KeywordSpotter
	ws           *sherpa.OnlineStream
	s2s          *sherpa.OnlineStream
	rs           *sherpa.OnlineStream // ungated diagnostic
	s2Until      float64
	Window       float64
	BreakGlassBy map[string]bool
	ring         []float32
	pos          int
	diag         bool
}

func NewPipeline(wakeThr, boost, s2Thr, s2Boost float64, diag bool) *Pipeline {
	p := &Pipeline{
		wake:         newSpotter("data/kw/wake.txt", wakeThr, boost),
		s2:           newSpotter("data/kw/stage2.txt", s2Thr, s2Boost),
		Window:       4.0,
		BreakGlassBy: map[string]bool{"ALFRED": true},
		diag:         diag,
	}
	p.Reset()
	return p
}

func (p *Pipeline) Reset() {
	if p.ws != nil {
		sherpa.DeleteOnlineStream(p.ws)
	}
	if p.s2s != nil {
		sherpa.DeleteOnlineStream(p.s2s)
		p.s2s = nil
	}
	if p.rs != nil {
		sherpa.DeleteOnlineStream(p.rs)
		p.rs = nil
	}
	p.ws = sherpa.NewKeywordStream(p.wake)
	if p.diag {
		p.rs = sherpa.NewKeywordStream(p.s2)
	}
	p.pos, p.ring, p.s2Until = 0, nil, 0
}

func (p *Pipeline) Close() {
	p.Reset()
	sherpa.DeleteOnlineStream(p.ws)
	sherpa.DeleteKeywordSpotter(p.wake)
	sherpa.DeleteKeywordSpotter(p.s2)
}

func drain(sp *sherpa.KeywordSpotter, s *sherpa.OnlineStream, t float64, kind string, out *[]Event) {
	for sp.IsReady(s) {
		sp.Decode(s)
		if r := sp.GetResult(s); r.Keyword != "" {
			*out = append(*out, Event{kind, strings.TrimSpace(r.Keyword), t})
			sp.Reset(s)
		}
	}
}

// Feed processes one chunk and returns the events it produced.
func (p *Pipeline) Feed(chunk []float32) []Event {
	var ev []Event
	p.pos += len(chunk)
	t := float64(p.pos) / sampleRate
	p.ring = append(p.ring, chunk...)
	if keep := sampleRate * 2; len(p.ring) > keep {
		p.ring = p.ring[len(p.ring)-keep:]
	}
	if p.rs != nil {
		p.rs.AcceptWaveform(sampleRate, chunk)
		var raw []Event
		drain(p.s2, p.rs, t, "raw", &raw)
		ev = append(ev, raw...)
	}
	if p.s2s != nil {
		if t > p.s2Until {
			sherpa.DeleteOnlineStream(p.s2s)
			p.s2s = nil
		} else {
			p.s2s.AcceptWaveform(sampleRate, chunk)
			var s2 []Event
			drain(p.s2, p.s2s, t, "s2", &s2)
			for _, e := range s2 {
				ev = append(ev, e, Event{"bg", e.Name, e.T})
			}
		}
	}
	p.ws.AcceptWaveform(sampleRate, chunk)
	var w []Event
	drain(p.wake, p.ws, t, "wake", &w)
	for _, e := range w {
		ev = append(ev, e)
		if p.BreakGlassBy[e.Name] {
			if p.s2s == nil {
				p.s2s = sherpa.NewKeywordStream(p.s2)
				// replay the last 2 s: wake detection lags the word by 0.4-0.7 s and stage 2 can start right on its heels
				p.s2s.AcceptWaveform(sampleRate, p.ring)
			}
			p.s2Until = t + p.Window
		}
	}
	return ev
}

// Flush pads with silence so the last words can be emitted.
func (p *Pipeline) Flush() []Event {
	return p.Feed(make([]float32, sampleRate*6/10))
}

// Run feeds a whole signal in 100 ms chunks.
func (p *Pipeline) Run(x []float32) []Event {
	const chunk = sampleRate / 10
	var ev []Event
	for i := 0; i < len(x); i += chunk {
		j := min(i+chunk, len(x))
		ev = append(ev, p.Feed(x[i:j])...)
	}
	return append(ev, p.Flush()...)
}

func cpuSeconds() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	f := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return f(ru.Utime) + f(ru.Stime)
}

func readWav(path string) []float32 {
	w := sherpa.ReadWave(path)
	if w == nil {
		must(fmt.Errorf("cannot read %s", path))
	}
	if w.SampleRate != sampleRate {
		must(fmt.Errorf("%s: want 16 kHz, got %d", path, w.SampleRate))
	}
	return w.Samples
}

func cmdKWS(args []string) {
	fs := flag.NewFlagSet("kws", flag.ExitOnError)
	wav := fs.String("wav", "", "16 kHz mono wav")
	thr := fs.Float64("thr", 0.25, "wake threshold")
	s2thr := fs.Float64("s2thr", 0.25, "stage-2 threshold")
	boost := fs.Float64("boost", 1.0, "wake keyword boost score")
	s2boost := fs.Float64("s2boost", 1.0, "stage-2 boost")
	diag := fs.Bool("diag", true, "also print ungated stage-2 hits")
	fs.Parse(args)
	x := readWav(*wav)
	p := NewPipeline(*thr, *boost, *s2thr, *s2boost, *diag)
	defer p.Close()
	for _, e := range p.Run(x) {
		fmt.Printf("%8.2fs  %-5s %s\n", e.T, e.Kind, e.Name)
	}
}

// hourResult is what `hour` reports per threshold.
type hourResult struct {
	Thr, CPU, Audio float64
	Wake            map[string]int
	S2              int
	BG              int
	Raw             int
	Events          []Event
}

func cmdHour(args []string) {
	fs := flag.NewFlagSet("hour", flag.ExitOnError)
	wav := fs.String("wav", "data/bg/hour.wav", "background audio")
	thrs := fs.String("thr", "0.1,0.25,0.4,0.6", "wake thresholds")
	s2thr := fs.Float64("s2thr", 0.25, "stage-2 threshold")
	s2boost := fs.Float64("s2boost", 1.0, "stage-2 boost")
	boost := fs.String("boost", "1.0", "boost score(s), one per -thr or a single value")
	diag := fs.Bool("diag", false, "also run ungated stage 2 (adds CPU)")
	list := fs.Bool("list", false, "print every event")
	fs.Parse(args)
	x := readWav(*wav)
	hours := float64(len(x)) / sampleRate / 3600
	fmt.Printf("audio: %.1f min\n\n| thr | %s | stage-2 spots (gated) | break-glass fires | CPU %% of 1 core | x realtime |\n", hours*60, "wake events (per hour)")
	fmt.Println("|---|---|---|---|---|---|")
	ths := parseFloats(*thrs)
	bs := broadcast(parseFloats(*boost), len(ths))
	for ti, thr := range ths {
		p := NewPipeline(thr, bs[ti], *s2thr, *s2boost, *diag)
		t0 := cpuSeconds()
		wall := time.Now()
		ev := p.Run(x)
		cpu := cpuSeconds() - t0
		_ = wall
		r := hourResult{Thr: thr, CPU: cpu, Audio: hours * 3600, Wake: map[string]int{}}
		for _, e := range ev {
			switch e.Kind {
			case "wake":
				r.Wake[e.Name]++
			case "s2":
				r.S2++
			case "bg":
				r.BG++
			case "raw":
				r.Raw++
			}
			if *list {
				fmt.Printf("  thr=%g %8.1fs %s %s\n", thr, e.T, e.Kind, e.Name)
			}
		}
		var parts []string
		tot := 0
		for _, k := range []string{"ALFRED", "JARVIS", "HEY_FOOTMAN", "HEY_JARVIS", "ALEXA", "HEY_MYCROFT"} {
			parts = append(parts, fmt.Sprintf("%s %d", strings.ToLower(k), r.Wake[k]))
			tot += r.Wake[k]
		}
		fmt.Printf("| b%g/t%g | %s (total %.1f/h) | %d | %d | %.2f%% | %.0fx |\n", bs[ti], thr, strings.Join(parts, ", "), float64(tot)/hours, r.S2, r.BG, 100*cpu/r.Audio, r.Audio/cpu)
		if *diag {
			fmt.Printf("|  | ungated stage-2 spots: %d |||||\n", r.Raw)
		}
		p.Close()
	}
}
