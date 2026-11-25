package sherpa

import (
	"testing"

	"github.com/irobinson010/footman/internal/registry"
)

func TestParakeetOptions(t *testing.T) {
	// The daemon passes the same keys to every STT engine.
	var o ParakeetOptions
	err := registry.Map{"model": "parakeet-tdt-0.6b-v2", "models_dir": "/m", "accelerator": "cpu",
		"language": "en", "cache_dir": "/c", "threads": 3}.Decode(&o)
	if err != nil {
		t.Fatal(err)
	}
	if o.ModelsDir != "/m" || o.Threads != 3 {
		t.Errorf("decoded %+v", o)
	}
	d := ParakeetOptions{}.withDefaults()
	if d.Model != "parakeet-tdt-0.6b-v2" || d.Provider != "cpu" || d.Threads < 1 || d.Threads > 16 {
		t.Errorf("defaults %+v", d)
	}
}

func TestSileroOptions(t *testing.T) {
	var o SileroOptions
	if err := (registry.Map{"models_dir": "/m", "min_silence": 0.8}).Decode(&o); err != nil {
		t.Fatal(err)
	}
	o = o.withDefaults()
	if o.MinSilence != 0.8 || o.Threshold != 0.5 || o.MinSpeech != 0.25 || o.MaxSpeech != 30 || o.Model != "silero-vad" || o.Threads != 1 {
		t.Errorf("options %+v", o)
	}
	if sileroWindow != 512 {
		t.Error("Silero VAD takes 512-sample windows at 16 kHz")
	}
}
