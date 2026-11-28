package main

import (
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
)

func TestWakeRejectedLine(t *testing.T) {
	r := core.WakeRejection{Turn: 7, Reason: core.WakeNoMatch, Audio: 800 * time.Millisecond, Took: 201 * time.Millisecond}
	const base = "turn 7: wake word not confirmed by speech-to-text (no_wake_word; 0.8 s checked in 201 ms), dropped"
	if got := wakeRejectedLine(r); got != base {
		t.Errorf("line = %q, want %q", got, base)
	}
	// With debug on, the transcript is added, quoted and cut.
	r.Text = "all fred open the door"
	if got, want := wakeRejectedLine(r), base+`; heard "all fred open the door"`; got != want {
		t.Errorf("debug line = %q, want %q", got, want)
	}
	r.Text = strings.Repeat("abcdefghij", 8)
	got := wakeRejectedLine(r)
	if want := `; heard "` + strings.Repeat("abcdefghij", 6) + `…"`; !strings.HasSuffix(got, want) {
		t.Errorf("long debug line = %q, want it to end %q", got, want)
	}
}

func TestTimingLine(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	full := core.TurnTiming{Turn: 4, Trigger: core.TriggerWake,
		WakeToEnd: ms(2310), Transcribe: ms(201), Ack: ms(410), FirstToken: ms(1450), FirstAudio: ms(3120)}
	if got, want := timingLine(full), "turn 4: timing (ms): wake->speech end 2310, transcribe 201, ack 410, first token 1450, first audio 3120; wake->first audio 5430"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	// Steps that did not happen are left out.
	key := core.TurnTiming{Turn: 5, Transcribe: ms(95), FirstAudio: ms(700)}
	if got, want := timingLine(key), "turn 5: timing (ms): transcribe 95, first audio 700"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// The wake earcon is on by default and speech.wake.debug is off; the debug
// aid is not something a voice command may change.
func TestWakeCueConfig(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	pc := pipelineConfig(cfg)
	if pc.NoEarcon || len(pc.Earcon.Samples) == 0 || len(pc.MissCue.Samples) == 0 || pc.WakeDebug {
		t.Errorf("defaults: NoEarcon=%v earcon=%d miss=%d debug=%v", pc.NoEarcon, len(pc.Earcon.Samples), len(pc.MissCue.Samples), pc.WakeDebug)
	}
	if k, ok := config.Lookup("speech.wake.debug"); !ok || k.VoiceSettable {
		t.Errorf("speech.wake.debug: found=%v voice-settable=%v, want a key that is not voice-settable", ok, k.VoiceSettable)
	}
	if k, ok := config.Lookup("speech.wake.earcon"); !ok || !k.VoiceSettable || k.Reload != config.Live {
		t.Errorf("speech.wake.earcon: found=%v %+v", ok, k)
	}
}
