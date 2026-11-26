package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
)

// frameSize is the rig's microphone frame: 20 ms.
const frameSize = 320

// speak queues microphone frames from a pattern, one 20 ms frame per
// character: 'x' is speech and '.' silence. Offsets run on from frame 0, so
// the fake wake word's frame indices and the VAD's segments line up.
func (r *rig) speak(pattern string) {
	var frames []audio.Frame
	for i, c := range pattern {
		level := float32(0)
		if c == 'x' {
			level = 0.5
		}
		s := make([]float32, frameSize)
		for j := range s {
			s[j] = level
		}
		frames = append(frames, audio.Frame{Format: audio.Speech, Samples: s, Offset: audio.Speech.Duration(i * frameSize)})
	}
	r.plat.Audio().(*pfake.Audio).QueueCapture(frames...)
}

func verifyRig(t *testing.T, wakeWords ...string) *rig {
	return newRig(t, func(_ *core.Deps, c *core.Config) {
		c.VerifyWake = true
		c.WakeWords = wakeWords
		c.MinCommand = 100 * time.Millisecond
	})
}

func repeat(c string, n int) string {
	out := ""
	for range n {
		out += c
	}
	return out
}

// With the second stage, the audio checked starts before the wake word (at
// the start of the segment, here) rather than just before its end, and a
// transcript that starts with a loose spelling of the wake word is acted on
// without it.
func TestVerifiedWakeIsActedOn(t *testing.T) {
	for _, text := range []string{"Ashferd, open YouTube", "Ashford open YouTube", "ford, open YouTube", "um Ashford, open YouTube"} {
		r := verifyRig(t, "ashford", "hey footman", "footman")
		r.speak(repeat("x", 60) + ".") // 1.2 s of speech
		r.wake.Schedule(40)            // the wake word ends at 820 ms
		r.transcript(text)
		r.run()
		equal(t, text+": opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
		in := r.stt.Inputs()
		if len(in) != 1 || len(in[0].Samples) != 60*frameSize {
			t.Errorf("%s: STT heard %d clips, want the whole 1.2 s segment", text, len(in))
		}
	}
}

// A transcript that does not start with a wake word is a false wake: it is
// dropped silently, reported without its text, and not published as a
// transcript.
func TestUnverifiedWakeIsDropped(t *testing.T) {
	for text, reason := range map[string]string{
		"and then we open the door": core.WakeNoMatch,
		"the Alfred Hitchcock film": core.WakeNoMatch,
		"":                          core.WakeNoSpeech,
	} {
		r := verifyRig(t, "alfred", "hey footman", "footman")
		rejected := bus.Subscribe(r.bus, core.WakeRejected, 4)
		transcripts := bus.Subscribe(r.bus, core.Transcribed, 4)
		outcomes := bus.Subscribe(r.bus, core.TurnDone, 4)
		r.speak(repeat("x", 30) + ".")
		r.wake.Schedule(10)
		r.transcript(text)
		r.run()
		got := await(t, rejected, func(core.WakeRejection) bool { return true })
		if got.Reason != reason || got.Audio != 600*time.Millisecond {
			t.Errorf("%q: rejection %+v, want %s for 600 ms", text, got, reason)
		}
		if len(r.launcher().Opened()) != 0 || len(r.input().Typed()) != 0 || len(r.ai.Requests()) != 0 {
			t.Errorf("%q: something was done", text)
		}
		if st := r.p.State(); st != core.Idle {
			t.Errorf("%q: state %v, want idle", text, st)
		}
		select {
		case ev := <-transcripts.C():
			t.Errorf("%q: published transcript %+v", text, ev.Data)
		case ev := <-outcomes.C():
			t.Errorf("%q: published outcome %+v", text, ev.Data)
		default:
		}
	}
}

// A wake word said on its own, then a pause, then the command: the wake
// word's audio is kept and checked together with the command.
func TestVerifiedWakeSaidAlone(t *testing.T) {
	r := verifyRig(t, "alfred", "footman")
	// "Alfred" (100 ms), a pause, then 400 ms of command.
	r.speak("xxxxx...." + repeat("x", 20) + ".")
	r.wake.Schedule(4)
	r.transcript("Alfred. Open YouTube.")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
	in := r.stt.Inputs()
	gap := audio.Speech.Samples(300 * time.Millisecond)
	if len(in) != 1 || len(in[0].Samples) != 25*frameSize+gap {
		t.Fatalf("STT heard %v, want the wake word, a 300 ms gap and the command", len(in))
	}
}

// The spotter can report the wake word after the VAD has already ended its
// segment; the segment is still the one checked.
func TestVerifiedWakeReportedLate(t *testing.T) {
	r := verifyRig(t, "alfred", "footman")
	// The VAD ends "Alfred" at the first silent frame (index 5); the
	// spotter reports it two frames later.
	r.speak("xxxxx...." + repeat("x", 20) + ".")
	r.wake.Schedule(7)
	r.transcript("Alfred, open YouTube")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
}

// The check uses the live wake list (SetWakeWords, as when the personas or
// speech.wake.extra change): pack words such as "hey ashford" and an extra
// "alfred" pass once set, and a word no longer listed does not.
func TestVerifyUsesLiveWakeWords(t *testing.T) {
	for _, tc := range []struct {
		text string
		live []string
		want bool
	}{
		{"Alfred, open YouTube", nil, false},
		{"Alfred, open YouTube", []string{"hey ashford", "alfred", "footman"}, true},
		{"Hey Ashford, open YouTube", []string{"hey ashford", "alfred", "footman"}, true},
		{"Ashferd, open YouTube", []string{"hey ashford", "alfred", "footman"}, true},
		{"Jeeves, open YouTube", []string{"hey ashford", "alfred", "footman"}, false},
	} {
		r := verifyRig(t, "jeeves", "footman")
		if tc.live != nil {
			r.p.SetWakeWords(tc.live)
		}
		r.speak(repeat("x", 30) + ".")
		r.wake.Schedule(10)
		r.transcript(tc.text)
		r.run()
		if got := len(r.launcher().URLs()) == 1; got != tc.want {
			t.Errorf("%q with %v: acted %v, want %v", tc.text, tc.live, got, tc.want)
		}
	}
}

// Speech nobody addressed is never transcribed, with or without the second
// stage; and without a wake word, the segment before it is never checked.
func TestVerifyTranscribesOnlyAfterTheSpotter(t *testing.T) {
	r := verifyRig(t, "alfred", "footman")
	r.speak(repeat("x", 30) + "." + repeat("x", 30) + ".")
	r.run()
	if n := len(r.stt.Inputs()); n != 0 {
		t.Errorf("STT called %d times without a wake word", n)
	}
}

// Without the second stage the command is cut from just before the end of
// the wake word, as before (ADR-0034), and not checked.
func TestWakeWithoutVerify(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.MinCommand = 100 * time.Millisecond })
	r.speak(repeat("x", 60) + ".")
	r.wake.Schedule(40)
	r.transcript("open youtube")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
	// From 600 ms before the wake word's end (820 ms) to 1.2 s.
	if in := r.stt.Inputs(); len(in) != 1 || len(in[0].Samples) != audio.Speech.Samples(980*time.Millisecond) {
		t.Errorf("STT heard %d samples", len(in[0].Samples))
	}
}

// The break-glass phrase still works through the check.
func TestVerifiedWakeBreakGlass(t *testing.T) {
	r := verifyRig(t, "alfred", "footman")
	stops := bus.Subscribe(r.bus, core.StopDone, 4)
	r.speak(repeat("x", 30) + ".")
	r.wake.Schedule(10)
	r.transcript("Alfred, stop everything.")
	// A stop mutes the microphone and Run stays up for the hotkeys.
	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	got := await(t, stops, func(core.Stopped) bool { return true })
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Run: %v", err)
	}
	if got.Source != core.StopVoice {
		t.Errorf("stop source %v", got.Source)
	}
}

// Each routed turn reports how it ended, without its text.
func TestTurnOutcome(t *testing.T) {
	r := newRig(t, nil)
	outcomes := bus.Subscribe(r.bus, core.TurnDone, 8)
	r.submit(core.TriggerWake, "footman, open youtube")
	if got := await(t, outcomes, func(core.Outcome) bool { return true }); got != (core.Outcome{Turn: 1, Trigger: core.TriggerWake, Route: core.RouteCommand, Handler: "open"}) {
		t.Errorf("outcome %+v", got)
	}
	r.ai.Queue(lfake.Reply{Text: "It's sunny."})
	r.submit(core.TriggerCommand, "what's the weather")
	if got := await(t, outcomes, func(core.Outcome) bool { return true }); got.Handler != core.HandlerAI || got.Err != "" || got.Route != core.RouteCommand {
		t.Errorf("outcome %+v", got)
	}
	r.submit(core.TriggerDictate, "hello")
	if got := await(t, outcomes, func(core.Outcome) bool { return true }); got.Route != core.RouteDictation || got.Handler != "" {
		t.Errorf("outcome %+v", got)
	}
}

// The spotter firing is published, with the keyword.
func TestWakeHeardIsPublished(t *testing.T) {
	r := newRig(t, nil)
	heard := bus.Subscribe(r.bus, core.WakeHeard, 4)
	r.wake.Keyword = "alfred"
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("open youtube")
	r.run()
	if got := await(t, heard, func(core.Wake) bool { return true }); got.Keyword != "alfred" || got.At != 20*time.Millisecond {
		t.Errorf("wake %+v", got)
	}
}
