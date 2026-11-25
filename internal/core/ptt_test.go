package core_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/speech"
)

// pttRig is a rig without a wake word, so the microphone opens only for
// push-to-talk, with a fake microphone that stays open like a real one.
type pttRig struct {
	*rig
	mic     *bus.Subscription[platform.TrayStatus]
	inserts *bus.Subscription[core.Insertion]
	fails   *bus.Subscription[core.Failure]
	done    chan error
	cancel  context.CancelFunc
}

func newPTT(t *testing.T, edit func(*core.Deps, *core.Config)) *pttRig {
	t.Helper()
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		d.Wake = nil
		c.ReleaseTail = time.Millisecond
		if edit != nil {
			edit(d, c)
		}
	})
	r.plat.Audio().(*pfake.Audio).KeepOpen()
	pr := &pttRig{
		rig:     r,
		mic:     bus.Subscribe(r.bus, core.MicChanged, 64),
		inserts: bus.Subscribe(r.bus, core.Inserted, 16),
		fails:   bus.Subscribe(r.bus, core.Failed, 16),
		done:    make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(r.ctx())
	pr.cancel = cancel
	go func() { pr.done <- r.p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-pr.done
	})
	// Hotkeys are registered before the microphone is reported off.
	await(t, pr.mic, func(s platform.TrayStatus) bool { return s == platform.MicOff })
	return pr
}

func (r *pttRig) awaitMic(want platform.TrayStatus) {
	r.t.Helper()
	await(r.t, r.mic, func(s platform.TrayStatus) bool { return s == want })
}

// Holding the dictation key records; releasing it ends the utterance, which
// is transcribed and typed. The microphone is open only in between.
func TestPushToTalkHold(t *testing.T) {
	r := newPTT(t, nil)
	if got := r.p.Mic().State; got != core.MicPushToTalk {
		t.Errorf("idle microphone state %q, want push_to_talk", got)
	}
	if n := r.speaker().Opened(); n != 0 {
		t.Fatalf("microphone opened %d times before any key", n)
	}
	// Silence, speech, silence (the speech's trailing frame), then a long
	// silence that trimming should drop.
	r.say(3)
	r.plat.Audio().(*pfake.Audio).QueueCapture(silence(60, 4)...)
	r.transcript("um, hello world")

	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	if got := r.p.Mic().State; got != core.MicRecording {
		t.Errorf("microphone state while held %q, want recording", got)
	}
	// Speech that ends while the key is held does not end the utterance.
	time.Sleep(50 * time.Millisecond)
	if n := len(r.stt.Inputs()); n != 0 {
		t.Fatalf("transcribed %d times before the key came up", n)
	}
	released := time.Now()
	r.input().Press("F8", false)
	ins := await(t, r.inserts, func(core.Insertion) bool { return true })
	if ins.Text != "hello world" || ins.Latency <= 0 || ins.Latency > time.Since(released) {
		t.Errorf("insertion %+v", ins)
	}
	r.awaitMic(platform.MicOff)
	equal(t, "typed", r.input().Typed(), []string{"hello world"})
	in := r.stt.Inputs()
	if len(in) != 1 {
		t.Fatalf("STT called %d times, want 1", len(in))
	}
	// 4 frames of speech (3 loud, 1 silent) of 20 ms, plus up to 0.5 s of
	// padding after: not the 60 silent frames.
	if d := in[0].Duration(); d < 60*time.Millisecond || d > 600*time.Millisecond {
		t.Errorf("transcribed %v of audio, want the speech trimmed", d)
	}
	equal(t, "states", r.path(), []core.State{
		core.Listening, core.Transcribing, core.Routing, core.Cleaning, core.Inserting, core.Idle,
	})
}

// silence returns n silent frames starting at frame index from.
func silence(n, from int) []audio.Frame {
	const size = 320
	out := make([]audio.Frame, n)
	for i := range out {
		out[i] = audio.Frame{Format: audio.Speech, Samples: make([]float32, size), Offset: audio.Speech.Duration((from + i) * size)}
	}
	return out
}

// A press and release with nothing said transcribes nothing.
func TestPushToTalkNoSpeech(t *testing.T) {
	r := newPTT(t, nil)
	r.plat.Audio().(*pfake.Audio).QueueCapture(silence(10, 0)...)
	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	r.input().Press("F8", false)
	if f := await(t, r.fails, func(core.Failure) bool { return true }); f.Err != core.ErrNoSpeech.Error() {
		t.Errorf("failure %+v", f)
	}
	r.awaitMic(platform.MicOff)
	if n := len(r.stt.Inputs()); n != 0 {
		t.Errorf("STT called %d times", n)
	}
	deadline := time.Now().Add(guard)
	for r.p.State() != core.Idle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := r.p.State(); got != core.Idle {
		t.Errorf("state %v, want idle", got)
	}
}

// In tap-to-talk mode a tap starts recording and the end of speech ends it,
// without a second tap.
func TestTapToTalkEndsAtEndOfSpeech(t *testing.T) {
	r := newPTT(t, func(_ *core.Deps, c *core.Config) { c.DictateMode = core.TapToTalk })
	r.say(3)
	r.transcript("tapped and spoken")
	r.input().Press("F8", true)
	r.input().Press("F8", false) // ignored in tap mode
	ins := await(t, r.inserts, func(core.Insertion) bool { return true })
	if ins.Text != "tapped and spoken" {
		t.Errorf("inserted %q", ins.Text)
	}
	r.awaitMic(platform.MicOff)
}

// A second tap ends a tap-to-talk recording while speech is still going on.
func TestTapToTalkSecondTap(t *testing.T) {
	r := newPTT(t, func(_ *core.Deps, c *core.Config) { c.DictateMode = core.TapToTalk })
	loud := make([]float32, 320)
	for i := range loud {
		loud[i] = 0.5
	}
	r.plat.Audio().(*pfake.Audio).QueueCapture(audio.Frame{Format: audio.Speech, Samples: loud})
	r.transcript("cut short")
	r.input().Press("F8", true)
	r.input().Press("F8", false)
	r.awaitMic(platform.Recording)
	time.Sleep(20 * time.Millisecond)
	if n := len(r.stt.Inputs()); n != 0 {
		t.Fatalf("transcribed before the second tap")
	}
	r.input().Press("F8", true)
	if ins := await(t, r.inserts, func(core.Insertion) bool { return true }); ins.Text != "cut short" {
		t.Errorf("inserted %q", ins.Text)
	}
}

// The command key records a command the same way.
func TestPushToTalkCommand(t *testing.T) {
	r := newPTT(t, nil)
	r.say(2)
	r.transcript("open youtube")
	r.input().Press("Shift+F8", true)
	r.awaitMic(platform.Recording)
	r.input().Press("Shift+F8", false)
	r.awaitMic(platform.MicOff)
	deadline := time.Now().Add(guard)
	for len(r.launcher().URLs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	equal(t, "opened", r.launcher().URLs(), []string{"https://www.youtube.com"})
}

// The stop hotkey discards a recording in progress and mutes; the next
// push-to-talk press lifts the mute and records again.
func TestStopDuringPushToTalk(t *testing.T) {
	r := newPTT(t, nil)
	stops := bus.Subscribe(r.bus, core.StopDone, 4)
	r.say(2)
	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	r.input().Press("Ctrl+F8", true)
	await(t, stops, func(core.Stopped) bool { return true })
	r.awaitMic(platform.MicOff)
	r.input().Press("F8", false) // the recording is gone: nothing happens
	if !r.p.Muted() {
		t.Fatal("not muted after the stop")
	}
	if n := len(r.stt.Inputs()); n != 0 {
		t.Fatalf("a stopped recording was transcribed")
	}

	r.say(2)
	r.transcript("after the stop")
	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	if r.p.Muted() {
		t.Error("a push-to-talk press did not lift the mute")
	}
	r.input().Press("F8", false)
	if ins := await(t, r.inserts, func(core.Insertion) bool { return true }); ins.Text != "after the stop" {
		t.Errorf("inserted %q", ins.Text)
	}
}

// A second utterance recorded while the first is still being handled waits
// for it rather than being dropped.
func TestPushToTalkQueuesBehindBusyTurn(t *testing.T) {
	block := make(chan struct{})
	r := newPTT(t, func(d *core.Deps, _ *core.Config) { d.STT = blockingSTT{d.STT, block} })
	r.say(2)
	r.transcript("first")
	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	r.input().Press("F8", false)
	await(t, r.states, func(c core.StateChange) bool { return c.To == core.Transcribing })

	r.say(2)
	r.transcript("second")
	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	r.input().Press("F8", false)
	r.awaitMic(platform.MicOff)
	close(block)
	await(t, r.inserts, func(i core.Insertion) bool { return i.Text == "first" })
	await(t, r.inserts, func(i core.Insertion) bool { return i.Text == "second" })
	equal(t, "typed", r.input().Typed(), []string{"first", "second"})
}

// holdingSTT records Hold calls (idle.STT has Hold).
type holdingSTT struct {
	speech.STT
	mu                        sync.Mutex
	held, released, whileHeld int
}

func (h *holdingSTT) Hold() func() {
	h.mu.Lock()
	h.held++
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		h.released++
		h.mu.Unlock()
	}
}

func (h *holdingSTT) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	h.mu.Lock()
	if h.held > h.released {
		h.whileHeld++
	}
	h.mu.Unlock()
	return h.STT.Transcribe(ctx, a)
}

// Speech-to-text is held from the key going down until the utterance is
// transcribed, so it is loaded while the user speaks and cannot be unloaded
// before the text is ready.
func TestPushToTalkHoldsSTT(t *testing.T) {
	var hs *holdingSTT
	r := newPTT(t, func(d *core.Deps, _ *core.Config) {
		hs = &holdingSTT{STT: d.STT}
		d.STT = hs
	})
	r.say(2)
	r.transcript("held")
	r.input().Press("F8", true)
	r.awaitMic(platform.Recording)
	r.input().Press("F8", false)
	await(t, r.inserts, func(core.Insertion) bool { return true })
	deadline := time.Now().Add(guard)
	for {
		hs.mu.Lock()
		held, released, whileHeld := hs.held, hs.released, hs.whileHeld
		hs.mu.Unlock()
		if held == 1 && released == 1 && whileHeld == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("held %d, released %d, transcribed while held %d", held, released, whileHeld)
		}
		time.Sleep(time.Millisecond)
	}
}

// blockingSTT waits for gate to close before transcribing.
type blockingSTT struct {
	speech.STT
	gate chan struct{}
}

func (b blockingSTT) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	select {
	case <-b.gate:
	case <-ctx.Done():
		return speech.Transcript{}, ctx.Err()
	}
	return b.STT.Transcribe(ctx, a)
}
