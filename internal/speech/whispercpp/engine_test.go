//go:build native && cgo

package whispercpp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

// The model and accelerator default to the CI setup (tiny.en on the CPU);
// FOOTMAN_TEST_WHISPER_MODEL and FOOTMAN_TEST_ACCEL override them.
func testOptions(t *testing.T) Options {
	model := os.Getenv("FOOTMAN_TEST_WHISPER_MODEL")
	if model == "" {
		model = "whisper-tiny.en"
	}
	acc := os.Getenv("FOOTMAN_TEST_ACCEL")
	if acc == "" {
		acc = CPU
	}
	return Options{Model: model, ModelsDir: speechtest.ModelsDir(t, model), Accelerator: acc, CacheDir: t.TempDir()}
}

func TestTranscribe(t *testing.T) {
	o := testOptions(t)
	start := time.Now()
	e, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	t.Logf("loaded in %s: %s", time.Since(start).Round(time.Millisecond), e.Info())

	clip := speechtest.Clip(t)
	start = time.Now()
	tr, err := e.Transcribe(context.Background(), clip)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transcribed %s in %s: %q (language %s, confidence %.2f)",
		clip.Duration(), time.Since(start).Round(time.Millisecond), tr.Text, tr.Language, tr.Confidence)
	if w := speechtest.WER(speechtest.Reference, tr.Text); w > 0.2 {
		t.Errorf("word error rate %.0f%%: %q", 100*w, tr.Text)
	}
	if tr.Language != "en" || tr.Confidence <= 0 {
		t.Errorf("language %q, confidence %v", tr.Language, tr.Confidence)
	}

	// Silence is fine, and so is no audio at all.
	if _, err := e.Transcribe(context.Background(), speech.Audio{Format: clip.Format}); err != nil {
		t.Errorf("empty audio: %v", err)
	}
}

func TestTranscribeCancelled(t *testing.T) {
	o := testOptions(t)
	e, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Transcribe(ctx, speechtest.Clip(t)); err == nil {
		t.Error("a cancelled context did not stop the transcription")
	}
	_ = e.Close()
	if _, err := e.Transcribe(context.Background(), speechtest.Clip(t)); err == nil {
		t.Error("transcribed after Close")
	}
}

func TestRegistered(t *testing.T) {
	o := testOptions(t)
	stt, err := speech.STTs.New(Name, registry.Map{"model": o.Model, "models_dir": o.ModelsDir, "accelerator": o.Accelerator})
	if err != nil {
		t.Fatal(err)
	}
	_ = stt.(*Engine).Close()
	if len(Accelerators()) == 0 || Accelerators()[len(Accelerators())-1] != CPU {
		t.Errorf("Accelerators() = %v, want it to end with cpu", Accelerators())
	}
}
