package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
)

// run runs the pipeline until the fake microphone's queued frames are used up.
func (r *rig) run() {
	r.t.Helper()
	if err := r.p.Run(r.ctx()); err != nil {
		r.t.Fatalf("Run: %v", err)
	}
}

// A wake word, then "open youtube": the fast path opens the site, with no AI
// and no dictation.
func TestWakeThenCommandReachesFastPath(t *testing.T) {
	r := newRig(t, nil)
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("open youtube")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
	if len(r.ai.Requests()) != 0 || len(r.input().Typed()) != 0 {
		t.Error("the command went to the AI or was typed")
	}
}

// The router strips the listening persona's own wake word, not just
// "footman", and wake + "dictate" is dictation.
func TestPersonaWakeWordIsStripped(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.WakeWords = []string{"alfred"} })
	r.wake.Keyword = "alfred"
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("Alfred dictate hello")
	r.run()
	equal(t, "typed", r.input().Typed(), []string{"hello"})

	r = newRig(t, func(_ *core.Deps, c *core.Config) { c.WakeWords = []string{"ashford"} })
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("Ashford, open YouTube")
	r.run()
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
}

// A wake word with nothing after it is dropped silently: nothing is
// transcribed, and the pipeline goes back to idle.
func TestWakeWithSilenceAfterIsDropped(t *testing.T) {
	for name, wakeAt := range map[string]int{
		"nothing said":       0,
		"only the wake word": 2, // speech that ended with the wake word
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			if name == "nothing said" {
				r.say(0)
			} else {
				r.say(3)
			}
			r.wake.Schedule(wakeAt)
			r.run()
			if n := len(r.stt.Inputs()); n != 0 {
				t.Errorf("STT called %d times, want 0", n)
			}
			if got := r.p.State(); got != core.Idle {
				t.Errorf("state %v, want idle", got)
			}
			if len(r.launcher().Opened()) != 0 || len(r.input().Typed()) != 0 {
				t.Error("something was done")
			}
		})
	}
}

func TestWakeEarcon(t *testing.T) {
	earcon := audio.Clip{Format: audio.Speech, Samples: []float32{0.3, 0.3, 0.3}}
	for _, quiet := range []bool{false, true} {
		r := newRig(t, func(_ *core.Deps, c *core.Config) { c.Earcon, c.Quiet = earcon, quiet })
		r.wake.Schedule(0)
		r.say(3)
		r.transcript("open youtube")
		r.run()
		// The earcon has 3 samples and the rig's acknowledgement 2.
		time.Sleep(20 * time.Millisecond)
		var earcons, acks int
		for _, c := range r.speaker().Played() {
			switch len(c.Samples) {
			case 3:
				earcons++
			case 2:
				acks++
			}
		}
		want := 1
		if quiet {
			want = 0
		}
		if earcons != want || acks != want {
			t.Errorf("quiet=%v: %d earcons and %d acknowledgements played, want %d of each", quiet, earcons, acks, want)
		}
	}
}

// Wake listening gives up the microphone while push-to-talk records, and
// takes it back afterwards: the device is never open twice.
func TestPushToTalkPausesWakeListening(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.ReleaseTail = time.Millisecond })
	r.speaker().KeepOpen()
	mic := bus.Subscribe(r.bus, core.MicChanged, 64)
	inserts := bus.Subscribe(r.bus, core.Inserted, 4)
	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	awaitMic := func(want platform.TrayStatus) {
		t.Helper()
		await(t, mic, func(s platform.TrayStatus) bool { return s == want })
	}

	awaitMic(platform.WakeListening)
	if n := r.speaker().Opened(); n != 1 {
		t.Fatalf("microphone opened %d times for wake listening, want 1", n)
	}
	r.say(2)
	r.transcript("hello world")
	r.input().Press("F8", true)
	awaitMic(platform.Recording)
	if got := r.p.Mic().State; got != core.MicRecording {
		t.Errorf("Mic = %v while recording", got)
	}
	for r.speaker().Undelivered() > 0 {
		time.Sleep(time.Millisecond)
	}
	r.input().Press("F8", false)
	await(t, inserts, func(core.Insertion) bool { return true })
	equal(t, "typed", r.input().Typed(), []string{"hello world"})

	// Wake listening comes back on its own, with a fresh microphone.
	deadline := time.Now().Add(guard)
	for r.speaker().Opened() < 3 || r.p.Mic().State != core.MicOn {
		if time.Now().After(deadline) {
			t.Fatalf("wake listening did not resume: opened %d, mic %v", r.speaker().Opened(), r.p.Mic())
		}
		time.Sleep(time.Millisecond)
	}
}

// The toggle closes the wake microphone and opens it again.
func TestSetWakeListening(t *testing.T) {
	r := newRig(t, nil)
	r.speaker().KeepOpen()
	mic := bus.Subscribe(r.bus, core.MicChanged, 64)
	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	await(t, mic, func(s platform.TrayStatus) bool { return s == platform.WakeListening })
	r.p.SetWakeListening(false)
	await(t, mic, func(s platform.TrayStatus) bool { return s == platform.MicOff })
	if r.p.WakeListening() || r.p.Mic().State != core.MicPushToTalk {
		t.Errorf("off: listening %v, mic %v", r.p.WakeListening(), r.p.Mic())
	}
	r.p.SetWakeListening(true)
	await(t, mic, func(s platform.TrayStatus) bool { return s == platform.WakeListening })
	if !r.p.WakeListening() || r.p.Mic().State != core.MicOn {
		t.Errorf("on: listening %v, mic %v", r.p.WakeListening(), r.p.Mic())
	}
}

// Quiet mode shows replies instead of speaking them.
func TestQuietRepliesAreNotSpoken(t *testing.T) {
	r := newRig(t, nil)
	r.p.SetQuiet(true)
	r.submit(core.TriggerWake, "footman, open youtube")
	if got := spoken(r.tts); len(got) != 0 {
		t.Errorf("spoke %v in quiet mode", got)
	}
	if !r.p.Quiet() {
		t.Error("Quiet() = false")
	}
}
