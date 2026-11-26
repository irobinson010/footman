//go:build native && cgo

package sherpa

// False-wake harness (issue #16, spikes/false-wake). It is skipped unless
// $FOOTMAN_FALSE_WAKE_DIR names a folder of 16 kHz mono WAVs laid out as
// <dir>/wav/<type>/*.wav (spikes/false-wake/fetch.py makes one) and
// $FOOTMAN_TEST_MODELS has the keyword-spotting model. It streams each file
// through the live path (AGC, then the keyword spotter, in 20 ms frames that
// the engine re-chunks to 100 ms) and writes every detection to a TSV.
//
//	FOOTMAN_FALSE_WAKE_DIR     data folder (required)
//	FOOTMAN_FALSE_WAKE_SETTINGS  "boost:threshold,..." (default "2:0.05")
//	FOOTMAN_FALSE_WAKE_KEYWORDS  comma list (default "ashford,alfred,hey footman")
//	FOOTMAN_FALSE_WAKE_AGC     "0" turns the AGC off (default on)
//	FOOTMAN_FALSE_WAKE_OUT     hits TSV (default <dir>/hits.tsv)
//	FOOTMAN_FALSE_WAKE_WORKERS parallel files (default 8)
//
// It also re-runs the positive synthetic clips of testdata/wake at each
// setting, so a candidate can be judged on both sides.

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
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

type fwSetting struct{ boost, threshold float64 }

func (s fwSetting) String() string { return fmt.Sprintf("b%g_t%g", s.boost, s.threshold) }

type fwHit struct {
	setting fwSetting
	typ     string
	file    string
	at      time.Duration
	kind    speech.WakeKind
	keyword string
}

func fwEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fwSettings(t *testing.T) []fwSetting {
	var out []fwSetting
	for _, s := range strings.Split(fwEnv("FOOTMAN_FALSE_WAKE_SETTINGS", "2:0.05"), ",") {
		b, th, ok := strings.Cut(strings.TrimSpace(s), ":")
		bf, err1 := strconv.ParseFloat(b, 64)
		tf, err2 := strconv.ParseFloat(th, 64)
		if !ok || err1 != nil || err2 != nil {
			t.Fatalf("bad setting %q, want boost:threshold", s)
		}
		out = append(out, fwSetting{bf, tf})
	}
	return out
}

func fwOpen(t *testing.T, dir string, keywords []string, s fwSetting) *KWS {
	k, err := OpenKWS(KWSOptions{Common: Common{ModelsDir: dir}, Keywords: keywords, Boost: float32(s.boost), Threshold: float32(s.threshold)})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// fwStream feeds a clip through the AGC (when on) and the spotter in 20 ms
// frames, like the daemon's capture loop, and returns the events.
func fwStream(k *KWS, c audio.Clip, agc bool) []speech.WakeEvent {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan audio.Frame, 16)
	go func() {
		defer close(in)
		for _, f := range c.Frames(audio.Speech.SampleRate / 50) {
			in <- f
		}
	}()
	var src <-chan audio.Frame = in
	if agc {
		src = audio.NewAGC(audio.AGCConfig{}).Stream(ctx, in)
	}
	var evs []speech.WakeEvent
	for e := range k.Listen(ctx, src) {
		evs = append(evs, e)
	}
	return evs
}

func TestFalseWake(t *testing.T) {
	dataDir := os.Getenv("FOOTMAN_FALSE_WAKE_DIR")
	if dataDir == "" {
		t.Skip("$FOOTMAN_FALSE_WAKE_DIR is not set")
	}
	models := speechtest.ModelsDir(t, kwsModel)
	settings := fwSettings(t)
	keywords := strings.Split(fwEnv("FOOTMAN_FALSE_WAKE_KEYWORDS", "ashford,alfred,hey footman"), ",")
	agc := fwEnv("FOOTMAN_FALSE_WAKE_AGC", "1") != "0"
	workers, _ := strconv.Atoi(fwEnv("FOOTMAN_FALSE_WAKE_WORKERS", "8"))
	outPath := fwEnv("FOOTMAN_FALSE_WAKE_OUT", filepath.Join(dataDir, "hits.tsv"))

	files, _ := filepath.Glob(filepath.Join(dataDir, "wav", "*", "*.wav"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no wav/<type>/*.wav under %s", dataDir)
	}

	var (
		mu    sync.Mutex
		hits  []fwHit
		secs  = map[string]float64{} // type -> seconds of audio
		fileC = make(chan string)
		wg    sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ks := make([]*KWS, len(settings))
			for i, s := range settings {
				ks[i] = fwOpen(t, models, keywords, s)
				defer ks[i].Close()
			}
			for p := range fileC {
				c, err := audio.ReadWAVFile(p)
				if err != nil {
					t.Error(err)
					continue
				}
				c = audio.ToSpeech(c)
				typ := filepath.Base(filepath.Dir(p))
				mu.Lock()
				secs[typ] += c.Duration().Seconds()
				mu.Unlock()
				for i, s := range settings {
					evs := fwStream(ks[i], c, agc)
					mu.Lock()
					for _, e := range evs {
						hits = append(hits, fwHit{s, typ, filepath.Base(p), e.At, e.Kind, e.Keyword})
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, p := range files {
		fileC <- p
	}
	close(fileC)
	wg.Wait()

	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.setting != b.setting {
			return a.setting.String() < b.setting.String()
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.at < b.at
	})
	var sb strings.Builder
	sb.WriteString("setting\ttype\tfile\tat_s\tkind\tkeyword\n")
	for _, h := range hits {
		kind := "wake"
		if h.kind == speech.WakeKindBreakGlass {
			kind = "breakglass"
		}
		fmt.Fprintf(&sb, "%s\t%s\t%s\t%.1f\t%s\t%s\n", h.setting, h.typ, h.file, h.at.Seconds(), kind, h.keyword)
	}
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var types []string
	total := 0.0
	for typ, s := range secs {
		types = append(types, typ)
		total += s
	}
	sort.Strings(types)
	t.Logf("%d files, %.2f h of audio, AGC %v, keywords %v, hits in %s", len(files), total/3600, agc, keywords, outPath)
	for _, s := range settings {
		t.Logf("setting boost %g threshold %g", s.boost, s.threshold)
		row := func(name string, secs float64, typ string) {
			wake, bg := 0, 0
			per := map[string]int{}
			for _, h := range hits {
				if h.setting != s || (typ != "" && h.typ != typ) {
					continue
				}
				if h.kind == speech.WakeKindWake {
					wake++
					per[h.keyword]++
				} else {
					bg++
				}
			}
			t.Logf("  %-8s %5.2f h  wake %3d (%5.2f/h)  break-glass %d  by keyword %v", name, secs/3600, wake, float64(wake)/(secs/3600), bg, per)
		}
		for _, typ := range types {
			row(typ, secs[typ], typ)
		}
		row("ALL", total, "")
	}

	// Positives at each setting: the synthetic clips of #74, no AGC, as the
	// engine test streams them.
	for _, s := range settings {
		k := fwOpen(t, models, []string{"alfred", "hey footman"}, s)
		for _, tc := range []struct{ prefix, keyword string }{{"alfred", "alfred"}, {"hey_footman", "hey footman"}} {
			clips := wakeClips(t, tc.prefix)
			hit := 0
			for _, p := range clips {
				if count(fwStream(k, loadClip(t, p), false), speech.WakeKindWake, tc.keyword) > 0 {
					hit++
				}
			}
			t.Logf("positives boost %g threshold %g: %q woke on %d of %d clips (%.0f%%)", s.boost, s.threshold, tc.keyword, hit, len(clips), 100*float64(hit)/float64(len(clips)))
		}
		for _, tc := range []struct{ prefix, phrase string }{{"alfred_stop", "stop everything"}, {"alfred_cancel", "cancel everything"}} {
			clips := wakeClips(t, tc.prefix)
			woke, bg := 0, 0
			for _, p := range clips {
				evs := fwStream(k, loadClip(t, p), false)
				if count(evs, speech.WakeKindWake, "alfred") > 0 {
					woke++
					if count(evs, speech.WakeKindBreakGlass, tc.phrase) > 0 {
						bg++
					}
				}
			}
			t.Logf("positives boost %g threshold %g: Alfred, %s: woke %d of %d, break-glass on %d", s.boost, s.threshold, tc.phrase, woke, len(clips), bg)
		}
		_ = k.Close()
	}
}
