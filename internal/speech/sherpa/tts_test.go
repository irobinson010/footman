//go:build native && cgo

package sherpa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/idle"
	"github.com/irobinson010/footman/internal/speech/models"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

// smallestTTS is the model the CI test synthesizes with (67 MB).
const smallestTTS = "piper-en-us-norman"

// TestTTSSynthesizes speaks a sentence through the registry with the
// smallest model, writes it to a WAV (never to the speakers), and checks
// cancellation, Close and a reload after Close (idle unloading).
func TestTTSSynthesizes(t *testing.T) {
	dir := speechtest.ModelsDir(t, smallestTTS, models.TTSLexicon)
	cfg := registry.Map{"models_dir": dir, "model": smallestTTS}
	tts, err := speech.TTSs.New(PiperName, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", tts.(*TTS).Info())
	start := time.Now()
	s, err := tts.Speak(context.Background(), "Very good, sir. It is 7:45 and the fare was £3.50.", speech.Voice{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Recv()
	if err != nil {
		t.Fatal(err)
	}
	ttfa := time.Since(start)
	rest, err := audio.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	clip := audio.Clip{Format: s.Format(), Samples: append(first.Samples, rest.Samples...)}
	t.Logf("first audio %s, %s of audio in %s", ttfa.Round(time.Millisecond), clip.Duration().Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	if clip.Format.SampleRate != 22050 || clip.Duration() < 2*time.Second || clip.Duration() > 10*time.Second {
		t.Errorf("format %+v, duration %s", clip.Format, clip.Duration())
	}
	writeWAV(t, filepath.Join(t.TempDir(), "tts.wav"), clip)

	// Cancelling stops the stream with the context's error.
	ctx, cancel := context.WithCancel(context.Background())
	s, err = tts.Speak(ctx, strings.Repeat("This sentence is here to be interrupted. ", 20), speech.Voice{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	for err == nil {
		_, err = s.Recv()
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("after cancel: %v", err)
	}
	_ = s.Close()

	// A stream left open is stopped by Close, which then frees the model.
	s, err = tts.Speak(context.Background(), strings.Repeat("Closing the engine stops me. ", 20), speech.Voice{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tts.(*TTS).Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = audio.ReadAll(s)
	if _, err := tts.Speak(context.Background(), "Hello.", speech.Voice{}); err == nil {
		t.Error("spoke after Close")
	}

	// Idle unloading builds the engine again with the same options.
	again, err := speech.TTSs.New(PiperName, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.(*TTS).Close() }()
	s, err = again.Speak(context.Background(), "Loaded again.", speech.Voice{Speaker: "anyone", Speed: 1.2})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := audio.ReadAll(s); err != nil || c.Duration() < 300*time.Millisecond {
		t.Errorf("after reload: %s, %v", c.Duration(), err)
	}
}

// TestTTSUnloads checks the engine under the daemon's idle unloading
// (cmd/footman/unload.go): it unloads when idle and loads again on use.
func TestTTSUnloads(t *testing.T) {
	dir := speechtest.ModelsDir(t, smallestTTS, models.TTSLexicon)
	cfg := registry.Map{"models_dir": dir, "model": smallestTTS}
	first, err := speech.TTSs.New(PiperName, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := idle.New("tts", PiperName, first, func() (speech.TTS, error) { return speech.TTSs.New(PiperName, cfg) }, idle.Options{})
	defer func() { _ = h.Close() }()
	tts := idle.TTS{H: h}
	say := func() {
		t.Helper()
		s, err := tts.Speak(context.Background(), "Still here.", speech.Voice{})
		if err != nil {
			t.Fatal(err)
		}
		if c, err := audio.ReadAll(s); err != nil || c.Duration() < 300*time.Millisecond {
			t.Fatalf("%s, %v", c.Duration(), err)
		}
	}
	say()
	if !h.Unload() {
		t.Fatal("did not unload")
	}
	if _, err := first.Speak(context.Background(), "Hi.", speech.Voice{}); err == nil {
		t.Error("the unloaded engine still speaks")
	}
	say()
	if st := h.State(); !st.Loaded || st.Loads != 2 {
		t.Errorf("state %+v", st)
	}
}

func TestTTSRejects(t *testing.T) {
	dir := speechtest.ModelsDir(t, smallestTTS, models.TTSLexicon)
	if _, err := OpenTTS(PiperName, TTSOptions{Common: Common{ModelsDir: t.TempDir()}}); err == nil {
		t.Error("opened with no models")
	}
	tts, err := OpenTTS(PiperName, TTSOptions{Common: Common{ModelsDir: dir}, Model: smallestTTS})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tts.Close() }()
	if _, err := tts.Speak(context.Background(), "Hi.", speech.Voice{ID: "kokoro-en-nope"}); err == nil {
		t.Error("unknown model accepted")
	}
}

func writeWAV(t testing.TB, path string, c audio.Clip) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := audio.EncodeWAV(f, c); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Bench sentences: short is a typical acknowledgement, long a typical
// spoken reply with the numbers and abbreviations the front end handles.
const (
	benchShort = "Very good, sir. The kettle is on."
	benchLong  = "I have tidied the study, filed the 3 reports you left on the desk, and booked the 7:45 train to London for Thursday the 3rd; the fare came to £42.50, and Dr. Pettigrew has confirmed the meeting."
)

// TestTTSBench measures time to first audio, real-time factor and memory on
// the CPU for each voice in $FOOTMAN_TTS_BENCH_CASES ("model@speaker" items,
// comma separated; a model may be a path), and writes the WAVs to
// $FOOTMAN_TTS_BENCH. With $FOOTMAN_TTS_ASR set to a models folder holding
// Parakeet, it also transcribes each long sample and reports the word error
// rate, a check on pronunciation. Run one case per process for clean memory
// numbers. Not run unless $FOOTMAN_TTS_BENCH is set.
func TestTTSBench(t *testing.T) {
	out := os.Getenv("FOOTMAN_TTS_BENCH")
	if out == "" {
		t.Skip("$FOOTMAN_TTS_BENCH is not set")
	}
	dir := os.Getenv(speechtest.ModelsEnv)
	threads, _ := strconv.Atoi(os.Getenv("FOOTMAN_TTS_THREADS"))
	for _, c := range strings.Split(os.Getenv("FOOTMAN_TTS_BENCH_CASES"), ",") {
		model, speaker, _ := strings.Cut(strings.TrimSpace(c), "@")
		rss0 := procStatus("VmRSS")
		start := time.Now()
		tts, err := OpenTTS(KokoroName, TTSOptions{Common: Common{ModelsDir: dir, Threads: threads}, Model: model, Speaker: speaker})
		if err != nil {
			t.Fatal(err)
		}
		load := time.Since(start)
		rssLoaded := procStatus("VmRSS")
		name := filepath.Base(model)
		if speaker != "" {
			name += "-" + speaker
		}
		var row []string
		var long audio.Clip
		for _, text := range []struct{ label, s string }{{"short", benchShort}, {"long", benchLong}} {
			var ttfas, rtfs []float64
			var clip audio.Clip
			for run := range 4 { // the first run warms up and is not counted
				st := time.Now()
				s, err := tts.Speak(context.Background(), text.s, speech.Voice{})
				if err != nil {
					t.Fatal(err)
				}
				f, err := s.Recv()
				if err != nil {
					t.Fatal(err)
				}
				ttfa := time.Since(st)
				rest, err := audio.ReadAll(s)
				if err != nil {
					t.Fatal(err)
				}
				total := time.Since(st)
				clip = audio.Clip{Format: s.Format(), Samples: append(f.Samples, rest.Samples...)}
				if run > 0 {
					ttfas = append(ttfas, ttfa.Seconds()*1000)
					rtfs = append(rtfs, total.Seconds()/clip.Duration().Seconds())
				}
			}
			writeWAV(t, filepath.Join(out, name+"-"+text.label+".wav"), clip)
			row = append(row, fmt.Sprintf("%s: first audio %.0f ms, RTF %.3f, %.1f s audio", text.label, median(ttfas), median(rtfs), clip.Duration().Seconds()))
			long = clip
		}
		if line := os.Getenv("FOOTMAN_TTS_SAMPLE"); line != "" {
			s, err := tts.Speak(context.Background(), line, speech.Voice{})
			if err != nil {
				t.Fatal(err)
			}
			c, err := audio.ReadAll(s)
			if err != nil {
				t.Fatal(err)
			}
			writeWAV(t, filepath.Join(out, name+"-line.wav"), c)
		}
		rssSynth, peak := procStatus("VmRSS"), procStatus("VmHWM")
		_ = tts.Close()
		if asr := os.Getenv("FOOTMAN_TTS_ASR"); asr != "" {
			p, err := OpenParakeet(ParakeetOptions{Common: Common{ModelsDir: asr}})
			if err != nil {
				t.Fatal(err)
			}
			tr, err := p.Transcribe(context.Background(), long)
			_ = p.Close()
			if err != nil {
				t.Fatal(err)
			}
			// Both sides go through the front end, so "7:45" and
			// "seven forty-five" compare equal.
			wer := speechtest.WER(NormalizeText(benchLong), NormalizeText(tr.Text))
			row = append(row, fmt.Sprintf("ASR WER %.0f%%: %q", 100*wer, tr.Text))
		}
		t.Logf("%s: load %s; RSS +%d MB loaded, +%d MB after synthesis, peak %d MB (process); %s", name, load.Round(time.Millisecond),
			(rssLoaded-rss0)>>10, (rssSynth-rss0)>>10, peak>>10, strings.Join(row, "; "))
	}
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// procStatus returns a /proc/self/status field in kB, or 0.
func procStatus(field string) int64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, field+":"); ok {
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return n
		}
	}
	return 0
}
