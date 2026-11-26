//go:build native && cgo

package sherpa

// Two-stage wake harness (issue #94, spikes/false-wake). Like TestFalseWake
// it is skipped unless $FOOTMAN_FALSE_WAKE_DIR is set; it also needs
// $FOOTMAN_TEST_MODELS to hold the keyword spotter, Silero VAD and Parakeet
// (all on the CPU). Each file runs through the real pipeline (core.Run with
// a fake microphone and desktop): AGC, then the spotter and the VAD, the
// command cut, Parakeet, and with verification the wake-word check
// (core.Router.MatchWake). It counts spotter hits and the turns that were
// acted on, and writes every second-stage transcript of the corpus to a TSV.
//
//	FOOTMAN_WAKE_VERIFY_SETTINGS  "boost:verify,..." (default "2:0,3:1,4:1")
//	FOOTMAN_WAKE_VERIFY_OUT       transcripts TSV (default <dir>/verify.tsv)
//	FOOTMAN_FALSE_WAKE_WORKERS    parallel files (default 8)
//	FOOTMAN_WAKE_VERIFY_CORPUS    "0" runs only the positives
//
// The positives are the synthetic "Alfred" and "Hey Footman" clips of #74
// followed by a command clip, with a short gap (one VAD segment, said at a
// natural pace) and a long one (the wake word alone, then the command). If
// <dir>/positives holds WAVs (spikes/false-wake/genpos.py), each is run too:
// the wake word and a command in one breath, at a brisk and a normal pace,
// counted per wake word by file-name prefix.
//
// Break-glass events from the spotter are counted and not passed on, so a
// stray one does not mute the rest of a file.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
)

type wvSetting struct {
	boost  float64
	verify bool
}

func (s wvSetting) String() string {
	v := "off"
	if s.verify {
		v = "on"
	}
	return fmt.Sprintf("boost %g, verify %s", s.boost, v)
}

// wvWake passes the spotter's wake events on and counts them; break-glass
// events are counted and dropped.
type wvWake struct {
	k                *KWS
	mu               sync.Mutex
	wakes, breakings int
	keywords         map[string]int
}

func (w *wvWake) Listen(ctx context.Context, frames <-chan speech.Frame) <-chan speech.WakeEvent {
	out := make(chan speech.WakeEvent)
	in := w.k.Listen(ctx, frames)
	go func() {
		defer close(out)
		for e := range in {
			w.mu.Lock()
			if e.Kind == speech.WakeKindBreakGlass {
				w.breakings++
				w.mu.Unlock()
				continue
			}
			w.wakes++
			w.keywords[e.Keyword]++
			w.mu.Unlock()
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// wvSTT records what it transcribed.
type wvSTT struct {
	stt   speech.STT
	mu    sync.Mutex
	texts []wvText
}

type wvText struct {
	text        string
	audio, took time.Duration
}

func (s *wvSTT) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	start := time.Now()
	t, err := s.stt.Transcribe(ctx, a)
	s.mu.Lock()
	s.texts = append(s.texts, wvText{t.Text, a.Duration(), time.Since(start)})
	s.mu.Unlock()
	return t, err
}

// wvResult is what one run of the pipeline over a clip did.
type wvResult struct {
	wakes, breakings int
	keywords         map[string]int
	acted            int            // routed turns (dictation or command)
	rejected         map[string]int // by reason
	none             int            // routed to none
	texts            []wvText
}

func (r *wvResult) add(o wvResult) {
	r.wakes += o.wakes
	r.breakings += o.breakings
	r.acted += o.acted
	r.none += o.none
	for k, v := range o.keywords {
		r.keywords[k] += v
	}
	for k, v := range o.rejected {
		r.rejected[k] += v
	}
	r.texts = append(r.texts, o.texts...)
}

func newWVResult() wvResult {
	return wvResult{keywords: map[string]int{}, rejected: map[string]int{}}
}

// wvRun streams c through a pipeline built like the daemon's.
func wvRun(t *testing.T, k *KWS, vad speech.VAD, stt speech.STT, keywords []string, s wvSetting, c audio.Clip) wvResult {
	b := bus.New()
	defer b.Close()
	plat, err := pfake.New(registry.Map{"root": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	wake := &wvWake{k: k, keywords: map[string]int{}}
	rec := &wvSTT{stt: stt}
	p, err := core.New(core.Deps{Bus: b, Platform: plat, VAD: vad, Wake: wake, STT: rec}, core.Config{
		WakeWords:  append(slicesClone(keywords), "footman"),
		AGC:        true,
		MinCommand: core.DefaultMinCommand,
		VerifyWake: s.verify,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	routes := bus.Subscribe(b, core.RouteChosen, 1024)
	rejects := bus.Subscribe(b, core.WakeRejected, 1024)
	res := newWVResult()
	var wg sync.WaitGroup
	wg.Go(func() {
		for ev := range routes.C() {
			if ev.Data.Route.Kind == core.RouteNone {
				res.none++
			} else {
				res.acted++
			}
		}
	})
	wg.Go(func() {
		for ev := range rejects.C() {
			res.rejected[ev.Data.Reason]++
		}
	})
	plat.Audio().(*pfake.Audio).QueueCapture(c.Frames(audio.Speech.SampleRate / 50)...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if err := p.Run(ctx); err != nil {
		t.Errorf("Run: %v", err)
	}
	routes.Close()
	rejects.Close()
	wg.Wait()
	wake.mu.Lock()
	res.wakes, res.breakings, res.keywords = wake.wakes, wake.breakings, wake.keywords
	wake.mu.Unlock()
	rec.mu.Lock()
	res.texts = rec.texts
	rec.mu.Unlock()
	return res
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

func wvSettings(t *testing.T) []wvSetting {
	var out []wvSetting
	for _, s := range strings.Split(fwEnv("FOOTMAN_WAKE_VERIFY_SETTINGS", "2:0,3:1,4:1"), ",") {
		b, v, ok := strings.Cut(strings.TrimSpace(s), ":")
		bf, err := strconv.ParseFloat(b, 64)
		if !ok || err != nil || (v != "0" && v != "1") {
			t.Fatalf("bad setting %q, want boost:0|1", s)
		}
		out = append(out, wvSetting{bf, v == "1"})
	}
	return out
}

func silence(d time.Duration) []float32 { return make([]float32, audio.Speech.Samples(d)) }

func TestWakeVerify(t *testing.T) {
	dataDir := os.Getenv("FOOTMAN_FALSE_WAKE_DIR")
	if dataDir == "" {
		t.Skip("$FOOTMAN_FALSE_WAKE_DIR is not set")
	}
	models := os.Getenv("FOOTMAN_TEST_MODELS")
	if models == "" {
		t.Skip("$FOOTMAN_TEST_MODELS is not set")
	}
	settings := wvSettings(t)
	keywords := strings.Split(fwEnv("FOOTMAN_FALSE_WAKE_KEYWORDS", "ashford,alfred,hey footman"), ",")
	workers, _ := strconv.Atoi(fwEnv("FOOTMAN_FALSE_WAKE_WORKERS", "8"))
	outPath := fwEnv("FOOTMAN_WAKE_VERIFY_OUT", filepath.Join(dataDir, "verify.tsv"))

	stt, err := OpenParakeet(ParakeetOptions{Common: Common{ModelsDir: models, Threads: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer stt.Close()
	t.Log(stt.Info())
	vadFor := func() *Silero {
		v, err := OpenSilero(SileroOptions{Common: Common{ModelsDir: models}})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Positives first: they are quick.
	cmds := wakeClips(t, "talk1")
	for _, s := range settings {
		k := fwOpen(t, models, keywords, fwSetting{s.boost, 0.05})
		vad := vadFor()
		for _, tc := range []struct{ prefix, name string }{{"alfred", "Alfred"}, {"hey_footman", "Hey Footman"}} {
			for _, gap := range []time.Duration{150 * time.Millisecond, 1200 * time.Millisecond} {
				clips := wakeClips(t, tc.prefix)
				total := newWVResult()
				passed := 0
				var sttWake, sttAudio time.Duration
				for i, path := range clips {
					w := loadClip(t, path)
					cmd := loadClip(t, cmds[i%len(cmds)])
					samples := silence(500 * time.Millisecond)
					samples = append(samples, w.Samples...)
					samples = append(samples, silence(gap)...)
					samples = append(samples, cmd.Samples...)
					samples = append(samples, silence(1500*time.Millisecond)...)
					r := wvRun(t, k, vad, stt, keywords, s, audio.Clip{Format: audio.Speech, Samples: samples})
					if r.acted > 0 {
						passed++
					}
					for _, x := range r.texts {
						sttWake += x.took
						sttAudio += x.audio
						t.Logf("  %s %s gap %v: %q (%.1f s audio, %d ms)", s, filepath.Base(path), gap, x.text, x.audio.Seconds(), x.took.Milliseconds())
					}
					total.add(r)
				}
				n := max(1, len(total.texts))
				t.Logf("positives %s: %s + command, gap %v: acted on %d of %d (spotter %d, rejected %v); STT %.0f ms for %.1f s audio on average",
					s, tc.name, gap, passed, len(clips), total.wakes, total.rejected,
					float64(sttWake.Milliseconds())/float64(n), sttAudio.Seconds()/float64(n))
			}
		}
		_ = k.Close()
	}

	// Natural-pace positives, wake word and command in one breath
	// (spikes/false-wake/genpos.py writes them to <dir>/positives).
	if pos, _ := filepath.Glob(filepath.Join(dataDir, "positives", "*.wav")); len(pos) > 0 {
		sort.Strings(pos)
		for _, s := range settings {
			k := fwOpen(t, models, keywords, fwSetting{s.boost, 0.05})
			vad := vadFor()
			type tally struct{ spotted, acted, n int }
			by := map[string]*tally{}
			var took, heard time.Duration
			checks := 0
			for _, path := range pos {
				name := filepath.Base(path)
				group := name[:strings.Index(name, "_s")]
				if by[group] == nil {
					by[group] = &tally{}
				}
				c := loadClip(t, path)
				samples := append(silence(500*time.Millisecond), c.Samples...)
				samples = append(samples, silence(1500*time.Millisecond)...)
				r := wvRun(t, k, vad, stt, keywords, s, audio.Clip{Format: audio.Speech, Samples: samples})
				g := by[group]
				g.n++
				if r.wakes > 0 {
					g.spotted++
				}
				if r.acted > 0 {
					g.acted++
				}
				for _, x := range r.texts {
					took += x.took
					heard += x.audio
					checks++
					if r.acted == 0 {
						t.Logf("  %s %s not acted on: %q", s, name, x.text)
					}
				}
			}
			for _, g := range []string{"ashford", "alfred", "hey_footman"} {
				if v := by[g]; v != nil {
					t.Logf("natural pace %s: %s: spotter %d of %d, acted on %d", s, g, v.spotted, v.n, v.acted)
				}
			}
			if checks > 0 {
				t.Logf("natural pace %s: STT %.0f ms for %.2f s audio on average", s, float64(took.Milliseconds())/float64(checks), heard.Seconds()/float64(checks))
			}
			_ = k.Close()
		}
	}
	if fwEnv("FOOTMAN_WAKE_VERIFY_CORPUS", "1") == "0" {
		return
	}

	files, _ := filepath.Glob(filepath.Join(dataDir, "wav", "*", "*.wav"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no wav/<type>/*.wav under %s", dataDir)
	}
	type fileRes struct {
		typ, file string
		secs      float64
		res       []wvResult // per setting
	}
	var (
		mu    sync.Mutex
		all   []fileRes
		fileC = make(chan string)
		wg    sync.WaitGroup
	)
	for range workers {
		wg.Go(func() {
			ks := make([]*KWS, len(settings))
			for i, s := range settings {
				ks[i] = fwOpen(t, models, keywords, fwSetting{s.boost, 0.05})
				defer ks[i].Close()
			}
			vad := vadFor()
			for p := range fileC {
				c, err := audio.ReadWAVFile(p)
				if err != nil {
					t.Error(err)
					continue
				}
				c = audio.ToSpeech(c)
				fr := fileRes{typ: filepath.Base(filepath.Dir(p)), file: filepath.Base(p), secs: c.Duration().Seconds()}
				for i, s := range settings {
					fr.res = append(fr.res, wvRun(t, ks[i], vad, stt, keywords, s, c))
				}
				mu.Lock()
				all = append(all, fr)
				mu.Unlock()
			}
		})
	}
	for _, p := range files {
		fileC <- p
	}
	close(fileC)
	wg.Wait()
	sort.Slice(all, func(i, j int) bool { return all[i].typ+all[i].file < all[j].typ+all[j].file })

	var sb strings.Builder
	sb.WriteString("setting\ttype\tfile\taudio_s\tstt_ms\ttranscript\n")
	for i, s := range settings {
		for _, fr := range all {
			for _, x := range fr.res[i].texts {
				fmt.Fprintf(&sb, "%s\t%s\t%s\t%.1f\t%d\t%s\n", s, fr.typ, fr.file, x.audio.Seconds(), x.took.Milliseconds(), strings.ReplaceAll(x.text, "\t", " "))
			}
		}
	}
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	secs := map[string]float64{}
	total := 0.0
	for _, fr := range all {
		secs[fr.typ] += fr.secs
		total += fr.secs
	}
	var types []string
	for typ := range secs {
		types = append(types, typ)
	}
	sort.Strings(types)
	t.Logf("%d files, %.2f h of audio, keywords %v, transcripts in %s", len(all), total/3600, keywords, outPath)
	for i, s := range settings {
		t.Logf("setting %s", s)
		row := func(name, typ string, h float64) {
			r := newWVResult()
			for _, fr := range all {
				if typ == "" || fr.typ == typ {
					r.add(fr.res[i])
				}
			}
			t.Logf("  %-8s %5.2f h  spotter %3d (%5.2f/h)  acted %3d (%5.2f/h)  rejected %v  routed to none %d  break-glass %d  by keyword %v",
				name, h, r.wakes, float64(r.wakes)/h, r.acted, float64(r.acted)/h, r.rejected, r.none, r.breakings, r.keywords)
		}
		for _, typ := range types {
			row(typ, typ, secs[typ]/3600)
		}
		row("ALL", "", total/3600)
	}
}
