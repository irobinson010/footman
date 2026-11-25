package speechtest

import (
	"testing"

	"github.com/irobinson010/footman/internal/audio"
)

func TestWER(t *testing.T) {
	for _, tc := range []struct {
		ref, hyp string
		want     float64
	}{
		{"a b c d", "A, b. c d!", 0},
		{"a b c d", "a x c d", 0.25},
		{"a b c d", "a b c", 0.25},
		{"a b", "a b c d", 1},
		{"", "", 0},
		{"", "x", 1},
	} {
		if got := WER(tc.ref, tc.hyp); got != tc.want {
			t.Errorf("WER(%q, %q) = %v, want %v", tc.ref, tc.hyp, got, tc.want)
		}
	}
}

func TestClip(t *testing.T) {
	c := Clip(t)
	if c.Format != audio.Speech || c.Duration().Seconds() != 5 {
		t.Errorf("clip is %v, %v", c.Format, c.Duration())
	}
}
