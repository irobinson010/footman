package audio_test

import (
	"math"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
)

func peak(c audio.Clip) float64 {
	var p float64
	for _, s := range c.Samples {
		p = max(p, math.Abs(float64(s)))
	}
	return p
}

func TestCues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		clip    audio.Clip
		maxPeak float64
	}{
		{"wake earcon", audio.WakeEarcon(audio.CueRate), 0.15},
		{"miss cue", audio.MissCue(audio.CueRate), 0.08},
	} {
		c := tc.clip
		if c.Format.SampleRate != audio.CueRate || c.Format.Channels != 1 {
			t.Errorf("%s: format %+v", tc.name, c.Format)
		}
		if d := c.Duration(); d < 120*time.Millisecond || d > 180*time.Millisecond {
			t.Errorf("%s: lasts %v, want 120 to 180 ms", tc.name, d)
		}
		if p := peak(c); p < 0.03 || p > tc.maxPeak {
			t.Errorf("%s: peak %.3f, want a quiet cue (0.03 to %.2f)", tc.name, p, tc.maxPeak)
		}
		// Faded in and out: no click at either end.
		if first, last := math.Abs(float64(c.Samples[0])), math.Abs(float64(c.Samples[len(c.Samples)-1])); first > 0.005 || last > 0.005 {
			t.Errorf("%s: starts at %.4f and ends at %.4f, want ~0", tc.name, first, last)
		}
	}
	if audio.MissCue(audio.CueRate).Samples[100] == audio.WakeEarcon(audio.CueRate).Samples[100] {
		t.Error("the miss cue sounds the same as the wake earcon")
	}
	if p, q := peak(audio.MissCue(audio.CueRate)), peak(audio.WakeEarcon(audio.CueRate)); p >= q {
		t.Errorf("miss cue peak %.3f is not softer than the earcon's %.3f", p, q)
	}
}

func TestCueRate(t *testing.T) {
	if c := audio.WakeEarcon(16000); c.Format.SampleRate != 16000 || c.Duration() < 140*time.Millisecond {
		t.Errorf("16 kHz earcon: %+v, %v", c.Format, c.Duration())
	}
	if c := audio.WakeEarcon(0); c.Format.SampleRate != audio.CueRate {
		t.Errorf("default rate = %d", c.Format.SampleRate)
	}
}
