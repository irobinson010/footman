package core

import (
	"testing"

	"github.com/irobinson010/footman/internal/audio"
)

func TestLevelStage(t *testing.T) {
	mk := func() <-chan audio.Frame {
		ch := make(chan audio.Frame, 300)
		for range 300 {
			s := make([]float32, 320)
			for i := range s {
				s[i] = 0.01 // about -40 dBFS
			}
			ch <- audio.Frame{Format: audio.Speech, Samples: s}
		}
		close(ch)
		return ch
	}
	last := func(ch <-chan audio.Frame) float32 {
		var v float32
		for f := range ch {
			v = f.Samples[0]
		}
		return v
	}
	off := &Pipeline{}
	if v := last(off.level(t.Context(), mk())); v != 0.01 {
		t.Fatalf("disabled stage changed audio: %v", v)
	}
	on := &Pipeline{cfg: Config{AGC: true}}
	if v := last(on.level(t.Context(), mk())); v < 0.05 {
		t.Fatalf("enabled stage did not raise a quiet signal: %v", v)
	}
}
