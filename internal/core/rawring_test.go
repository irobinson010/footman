package core

import (
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
)

// ramp is a frame of n samples counting up from first, at stream sample first.
func ramp(first, n int) audio.Frame {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(first + i)
	}
	return audio.Frame{Format: audio.Speech, Samples: s, Offset: audio.Speech.Duration(first)}
}

func TestRawRingSlice(t *testing.T) {
	r := newRawRing(100 * time.Millisecond) // 1600 samples
	for i := range 10 {                     // 3200 samples: wraps twice
		r.write(ramp(i*320, 320))
	}
	// Only the newest 1600 samples (1600..3199) are held.
	got, start := r.slice(0, audio.Speech.Duration(4000))
	if len(got) != 1600 || start != audio.Speech.Duration(1600) || got[0] != 1600 || got[1599] != 3199 {
		t.Fatalf("slice kept %d samples from %v", len(got), start)
	}
	got, start = r.slice(audio.Speech.Duration(2000), audio.Speech.Duration(2500))
	if len(got) != 500 || start != audio.Speech.Duration(2000) || got[0] != 2000 || got[499] != 2499 {
		t.Fatalf("slice returned %d samples from %v", len(got), start)
	}
	if got, _ := r.slice(audio.Speech.Duration(3200), audio.Speech.Duration(4000)); got != nil {
		t.Errorf("slice past the end returned %d samples", len(got))
	}
	if got, _ := r.slice(0, audio.Speech.Duration(1000)); got != nil {
		t.Errorf("slice of audio already dropped returned %d samples", len(got))
	}
}

// A gap in the stream starts the ring afresh, so audio from either side of
// it is never joined.
func TestRawRingGap(t *testing.T) {
	r := newRawRing(time.Second)
	r.write(ramp(0, 320))
	r.write(ramp(1000, 320))
	got, start := r.slice(0, audio.Speech.Duration(5000))
	if len(got) != 320 || start != audio.Speech.Duration(1000) {
		t.Errorf("slice returned %d samples from %v, want the 320 after the gap", len(got), start)
	}
}
