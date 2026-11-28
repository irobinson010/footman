package core_test

import (
	"testing"

	"github.com/irobinson010/footman/internal/audio"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
)

// speakLevels is speak with a level per character: 'w' is a wake word too
// quiet for the fake VAD (a noise-gated or clipped onset), 'x' is speech and
// '.' silence.
func (r *rig) speakLevels(pattern string) {
	levels := map[rune]float32{'w': 0.05, 'x': 0.5, '.': 0}
	var frames []audio.Frame
	for i, c := range pattern {
		s := make([]float32, frameSize)
		for j := range s {
			s[j] = levels[c]
		}
		frames = append(frames, audio.Frame{Format: audio.Speech, Samples: s, Offset: audio.Speech.Duration(i * frameSize)})
	}
	r.plat.Audio().(*pfake.Audio).QueueCapture(frames...)
}

// frameLevels reads each 20 ms frame of a clip back as one of the characters
// speakLevels takes.
func frameLevels(a audio.Clip) string {
	out := ""
	for i := 0; i+frameSize <= len(a.Samples); i += frameSize {
		switch a.Samples[i] {
		case 0.05:
			out += "w"
		case 0.5:
			out += "x"
		default:
			out += "."
		}
	}
	return out
}

// The spotter heard the wake word but the VAD put none of it in a segment (a
// noise-gated headset, a short word): the second stage is still given the
// raw audio around the wake word, then a gap, then the command, so a good
// command is not dropped (ADR-0050).
func TestVerifiedWakeMissedByVAD(t *testing.T) {
	r := verifyRig(t, "alfred", "footman")
	r.speakLevels("wwwww..." + repeat("x", 20) + ".")
	r.wake.Schedule(4)
	r.transcript("Alfred, open YouTube")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
	in := r.stt.Inputs()
	if len(in) != 1 {
		t.Fatalf("STT heard %d clips, want 1", len(in))
	}
	// 5 frames of wake word, a 300 ms gap (15 frames), 20 of command.
	want := "wwwww" + repeat(".", 15) + repeat("x", 20)
	if got := frameLevels(in[0]); got != want {
		t.Errorf("STT heard %q, want %q", got, want)
	}
}

// The VAD's segment starts just after the wake word, which is in none of it:
// the raw audio joins straight on to the segment.
func TestVerifiedWakeClippedByVAD(t *testing.T) {
	r := verifyRig(t, "alfred", "footman")
	r.speakLevels("wwwww" + repeat("x", 20) + ".")
	r.wake.Schedule(4)
	r.transcript("Alfred, open YouTube")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
	in := r.stt.Inputs()
	if len(in) != 1 {
		t.Fatalf("STT heard %d clips, want 1", len(in))
	}
	if got, want := frameLevels(in[0]), "wwwww"+repeat("x", 20); got != want {
		t.Errorf("STT heard %q, want %q", got, want)
	}
}
