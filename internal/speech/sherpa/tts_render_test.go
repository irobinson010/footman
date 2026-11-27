//go:build native && cgo

package sherpa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/speech"
)

// TestRenderSentences renders each line of $FOOTMAN_RENDER_SENTENCES with the
// model in $FOOTMAN_RENDER_MODEL (a model folder or .onnx) on the CPU, writes
// NN.wav files to $FOOTMAN_RENDER_OUT (never to the speakers), and logs each
// line's time to first audio and real-time factor (median of 3 timed runs
// after a warm-up), plus load time and process memory. It is the S0-10 render
// and measurement tool for fine-tuned Piper voices. $FOOTMAN_RENDER_LEXICON_DIR
// is the models folder holding the shared lexicon (default: the usual
// locations). Not run unless $FOOTMAN_RENDER_OUT is set.
func TestRenderSentences(t *testing.T) {
	out := os.Getenv("FOOTMAN_RENDER_OUT")
	if out == "" {
		t.Skip("$FOOTMAN_RENDER_OUT is not set")
	}
	raw, err := os.ReadFile(os.Getenv("FOOTMAN_RENDER_SENTENCES"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	threads, _ := strconv.Atoi(os.Getenv("FOOTMAN_TTS_THREADS"))
	rss0 := procStatus("VmRSS")
	start := time.Now()
	engine := os.Getenv("FOOTMAN_RENDER_ENGINE") // default piper; "kokoro" for the A/B baseline
	if engine == "" {
		engine = PiperName
	}
	tts, err := OpenTTS(engine, TTSOptions{
		Common:  Common{ModelsDir: os.Getenv("FOOTMAN_RENDER_LEXICON_DIR"), Threads: threads},
		Model:   os.Getenv("FOOTMAN_RENDER_MODEL"),
		Speaker: os.Getenv("FOOTMAN_RENDER_SPEAKER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tts.Close() }()
	load := time.Since(start)
	var totalAudio, totalWall float64
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
		var ttfas, rtfs []float64
		var clip audio.Clip
		for run := range 4 { // the first run warms up and is not counted
			st := time.Now()
			s, err := tts.Speak(context.Background(), line, speech.Voice{})
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
				totalAudio += clip.Duration().Seconds()
				totalWall += total.Seconds()
			}
		}
		writeWAV(t, filepath.Join(out, fmt.Sprintf("%02d.wav", n)), clip)
		// What the model is asked to say, for an ASR check of the render.
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("%02d.txt", n)), []byte(NormalizeText(line)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%02d: first audio %.0f ms, RTF %.3f, %.1f s audio: %s", n, median(ttfas), median(rtfs), clip.Duration().Seconds(), line)
	}
	t.Logf("load %s; RSS +%d MB after synthesis, peak %d MB (process); overall RTF %.3f over %.0f s of audio",
		load.Round(time.Millisecond), (procStatus("VmRSS")-rss0)>>10, procStatus("VmHWM")>>10, totalWall/totalAudio, totalAudio)
}
