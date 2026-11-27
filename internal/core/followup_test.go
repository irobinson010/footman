package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
)

// fakeTimers stands in for the follow-up window's timer: a window waits on
// the channel After returned, and the test fires it.
type fakeTimers struct {
	mu    sync.Mutex
	chans []chan time.Time
	asked []time.Duration
}

func (f *fakeTimers) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.chans = append(f.chans, ch)
	f.asked = append(f.asked, d)
	return ch
}

// fireLast times out the most recently opened window.
func (f *fakeTimers) fireLast() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chans[len(f.chans)-1] <- time.Time{}
}

func (f *fakeTimers) lastAsked() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[len(f.asked)-1]
}

func (f *fakeTimers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chans)
}

// followRig is a rig whose microphone stays open and takes speech at any
// stream offset, with the pipeline running.
type followRig struct {
	*rig
	timers *fakeTimers
	turns  *bus.Subscription[core.Outcome]
	said   *bus.Subscription[core.Transcript]
	spoke  *bus.Subscription[core.SpeechStart]
}

func newFollowRig(t *testing.T, edit func(*core.Deps, *core.Config)) *followRig {
	t.Helper()
	timers := &fakeTimers{}
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		c.FollowUp, c.After = true, timers.After
		if edit != nil {
			edit(d, c)
		}
	})
	r.speaker().Live()
	return &followRig{
		rig: r, timers: timers,
		turns: bus.Subscribe(r.bus, core.TurnDone, 64),
		said:  bus.Subscribe(r.bus, core.Transcribed, 64),
		spoke: bus.Subscribe(r.bus, core.SpeechStarted, 64),
	}
}

// start runs the pipeline until the test ends.
func (f *followRig) start() {
	ctx, cancel := context.WithCancel(f.ctx())
	done := make(chan error, 1)
	go func() { done <- f.p.Run(ctx) }()
	f.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			f.t.Errorf("Run: %v", err)
		}
	})
}

// speak queues n frames of speech, then silence to end it, whose first frame
// is at stream offset at, and waits until the pipeline has read them.
func (f *followRig) speak(at time.Duration, n int) {
	const size = 320
	var frames []audio.Frame
	off := at
	for i := range n + 3 {
		level := float32(0.5)
		if i >= n {
			level = 0
		}
		s := make([]float32, size)
		for j := range s {
			s[j] = level
		}
		frames = append(frames, audio.Frame{Format: audio.Speech, Samples: s, Offset: off})
		off += audio.Speech.Duration(size)
	}
	f.speaker().QueueCapture(frames...)
	f.drain()
}

// drain waits until the microphone has been read to the end, which, as the
// stages are unbuffered, means the pipeline has seen what came before.
func (f *followRig) drain() {
	f.t.Helper()
	deadline := time.Now().Add(guard)
	for f.speaker().Undelivered() > 0 {
		if time.Now().After(deadline) {
			f.t.Fatal("the pipeline did not read the microphone")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // the last segment is claimed and handed on
}

func (f *followRig) awaitState(to core.State) {
	f.t.Helper()
	await(f.t, f.states, func(s core.StateChange) bool { return s.To == to })
}

func (f *followRig) awaitTurn() core.Outcome {
	f.t.Helper()
	return await(f.t, f.turns, func(core.Outcome) bool { return true })
}

// command says "footman, <text>" at the start of the stream: the wake word
// after the first frame, then speech.
func (f *followRig) command(text string, replies ...lfake.Reply) {
	f.wake.Schedule(0)
	f.say(3)
	f.transcript(text)
	f.ai.Queue(replies...)
	f.start()
}

// sawState reports whether the pipeline has entered s, draining the states.
func (f *followRig) sawState(s core.State) bool {
	for _, got := range f.path() {
		if got == s {
			return true
		}
	}
	return false
}

// A reply that ends with a question opens the window, the answer needs no
// wake word, and it is the same conversation.
func TestFollowUpAnswerIsRoutedWithoutWakeWord(t *testing.T) {
	f := newFollowRig(t, nil)
	f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "Sunny in Raleigh."})
	f.awaitState(core.FollowUp)
	f.awaitTurn()

	f.transcript("Raleigh")
	f.speak(2*time.Second, 3)
	tr := await(t, f.said, func(tr core.Transcript) bool { return tr.Text == "Raleigh" })
	if tr.Trigger != core.TriggerFollowUp {
		t.Errorf("trigger %v, want followup", tr.Trigger)
	}
	if out := f.awaitTurn(); out.Err != "" || out.Handler != core.HandlerAI {
		t.Errorf("outcome %+v", out)
	}
	equal(t, "spoken", spoken(f.tts), []string{"Which city?", "Sunny in Raleigh."})

	reqs := f.ai.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d AI requests, want 2", len(reqs))
	}
	var got []string
	for _, m := range reqs[1].Messages {
		got = append(got, string(m.Role)+": "+m.Text)
	}
	equal(t, "second request", got, []string{"user: what's the weather", "assistant: Which city?", "user: Raleigh"})
	// A statement closes it: the state returns to idle, and nothing more
	// is heard.
	f.awaitState(core.Idle)
	f.transcript("ignored")
	f.speak(5*time.Second, 3)
	if n := len(f.stt.Inputs()); n != 2 {
		t.Errorf("speech-to-text heard %d utterances, want 2", n)
	}
}

// A reply that does not end with a question opens nothing.
func TestFollowUpNotOpenedAfterStatement(t *testing.T) {
	f := newFollowRig(t, nil)
	f.command("what's the weather", lfake.Reply{Text: "It is sunny. Would you like a hat? Never mind, it is sunny."})
	f.awaitTurn()
	f.awaitState(core.Idle)
	f.transcript("ignored")
	f.speak(2*time.Second, 3)
	if f.sawState(core.FollowUp) || f.timers.count() != 0 {
		t.Error("a window opened after a statement")
	}
	if n := len(f.stt.Inputs()); n != 1 {
		t.Errorf("speech-to-text heard %d utterances, want 1", n)
	}
}

// With speech.followup.enabled off there is no window, and switching it on
// again while running brings it back.
func TestFollowUpSwitch(t *testing.T) {
	f := newFollowRig(t, func(_ *core.Deps, c *core.Config) { c.FollowUp = false })
	f.command("what's the weather", lfake.Reply{Text: "Which city?"})
	f.awaitTurn()
	f.awaitState(core.Idle)
	if f.sawState(core.FollowUp) {
		t.Fatal("a window opened with follow-up off")
	}
	f.p.SetFollowUp(true, time.Second)
	f.ai.Queue(lfake.Reply{Text: "Which day?"})
	if err := f.p.Submit(f.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "footman, weather"}); err != nil {
		t.Fatal(err)
	}
	f.awaitState(core.FollowUp)
	if got := f.timers.lastAsked(); got != time.Second+core.DefaultFollowUpGuard {
		t.Errorf("window waits %v, want the new timeout plus the guard", got)
	}
}

// The window closes after the timeout, and speech after it is not heard.
func TestFollowUpTimesOut(t *testing.T) {
	f := newFollowRig(t, nil)
	f.command("what's the weather", lfake.Reply{Text: "Which city?"})
	f.awaitState(core.FollowUp)
	f.awaitTurn()
	if got := f.timers.lastAsked(); got != core.DefaultFollowUpTimeout+core.DefaultFollowUpGuard {
		t.Errorf("window waits %v, want %v", got, core.DefaultFollowUpTimeout+core.DefaultFollowUpGuard)
	}
	f.timers.fireLast()
	f.awaitState(core.Idle)

	// Speech from after the deadline is not an answer.
	f.transcript("Raleigh")
	f.speak(30*time.Second, 3)
	if n := len(f.stt.Inputs()); n != 1 {
		t.Errorf("speech-to-text heard %d utterances, want 1", n)
	}
}

// Speech that began before the deadline but ends after it still counts.
func TestFollowUpHearsSpeechInProgressAtTheDeadline(t *testing.T) {
	f := newFollowRig(t, nil)
	f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "Sunny."})
	f.awaitState(core.FollowUp)
	f.awaitTurn()
	f.timers.fireLast()
	f.awaitState(core.Idle)
	f.transcript("Raleigh")
	f.speak(2*time.Second, 3) // began inside the window
	await(t, f.said, func(tr core.Transcript) bool { return tr.Text == "Raleigh" })
}

// The microphone does not open while Footman is talking: speech during the
// reply, and in the guard after it, is not an answer.
func TestFollowUpWaitsForTheSpeechAndTheGuard(t *testing.T) {
	f := newFollowRig(t, nil)
	release := f.speaker().HoldPlayback(100)
	defer release()
	f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "Sunny."})
	await(t, f.spoke, func(core.SpeechStart) bool { return true })

	// The owner talks over it.
	f.transcript("over it")
	f.speak(time.Second, 3)
	if f.sawState(core.FollowUp) {
		t.Fatal("the window opened while Footman was speaking")
	}
	if n := len(f.stt.Inputs()); n != 1 {
		t.Fatalf("speech-to-text heard %d utterances during the reply, want 1", n)
	}

	release()
	f.awaitState(core.FollowUp)
	// Inside the guard after the last frame: still ignored.
	f.speak(time.Second+200*time.Millisecond, 3)
	if n := len(f.stt.Inputs()); n != 1 {
		t.Fatalf("speech-to-text heard %d utterances in the guard, want 1", n)
	}
	// After it: heard.
	f.speak(3*time.Second, 3)
	await(t, f.said, func(tr core.Transcript) bool { return tr.Trigger == core.TriggerFollowUp })
}

// A chained question re-opens the window.
func TestFollowUpReopensOnAChainedQuestion(t *testing.T) {
	f := newFollowRig(t, nil)
	f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "Which state?"}, lfake.Reply{Text: "Sunny in Raleigh, North Carolina."})
	f.awaitState(core.FollowUp)
	f.awaitTurn()

	f.transcript("Raleigh")
	f.speak(2*time.Second, 3)
	f.awaitTurn()
	f.awaitState(core.FollowUp) // asked again
	if f.timers.count() != 2 {
		t.Errorf("%d windows opened, want 2", f.timers.count())
	}
	f.transcript("North Carolina")
	f.speak(4*time.Second, 3)
	f.awaitTurn()
	equal(t, "spoken", spoken(f.tts), []string{"Which city?", "Which state?", "Sunny in Raleigh, North Carolina."})
	var got []string
	for _, m := range f.ai.Requests()[2].Messages {
		got = append(got, m.Text)
	}
	equal(t, "third request", got, []string{"what's the weather", "Which city?", "Raleigh", "Which state?", "North Carolina"})
}

// "Never mind", "that's all" and "cancel" close the window without a reply.
func TestFollowUpClosesOnNeverMind(t *testing.T) {
	for _, phrase := range []string{"never mind", "That's all.", "cancel", "Nevermind"} {
		t.Run(phrase, func(t *testing.T) {
			f := newFollowRig(t, nil)
			f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "should not be asked"})
			f.awaitState(core.FollowUp)
			f.awaitTurn()
			f.transcript(phrase)
			f.speak(2*time.Second, 3)
			await(t, f.said, func(tr core.Transcript) bool { return tr.Text == phrase })
			f.awaitState(core.Idle)
			if n := len(f.ai.Requests()); n != 1 {
				t.Errorf("%d AI requests, want 1: %q went on to the AI", n, phrase)
			}
			equal(t, "spoken", spoken(f.tts), []string{"Which city?"})
			// And it really is closed.
			f.transcript("Raleigh")
			f.speak(5*time.Second, 3)
			if n := len(f.stt.Inputs()); n != 2 {
				t.Errorf("speech-to-text heard %d utterances, want 2", n)
			}
		})
	}
}

// The break-glass stop closes the window and drops the conversation.
func TestFollowUpClosesOnBreakGlass(t *testing.T) {
	f := newFollowRig(t, nil)
	f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "should not be asked"})
	f.awaitState(core.FollowUp)
	f.awaitTurn()
	f.p.Stop()
	f.awaitState(core.Idle)
	if got := f.p.Mic().State; got != core.MicMuted {
		t.Errorf("microphone %v after the stop", got)
	}
	f.wake.At = nil // the fake wake word fires on the first frame of every session
	f.p.Unmute()
	for f.speaker().Opened() < 2 || f.p.Mic().State != core.MicOn {
		time.Sleep(time.Millisecond)
	}
	f.transcript("Raleigh")
	f.speak(3*time.Second, 3)
	if n := len(f.stt.Inputs()); n != 1 {
		t.Errorf("speech-to-text heard %d utterances, want 1", n)
	}
}

// A wake word said in the window starts a command of its own, not an answer.
func TestFollowUpYieldsToTheWakeWord(t *testing.T) {
	f := newFollowRig(t, nil)
	f.wake.Schedule(4) // the first frame of the speech after the first command
	f.command("what's the weather", lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "Opened."})
	f.awaitState(core.FollowUp)
	f.awaitTurn()
	f.transcript("footman, open youtube")
	f.speak(2*time.Second, 3)
	tr := await(t, f.said, func(tr core.Transcript) bool { return tr.Text == "footman, open youtube" })
	if tr.Trigger != core.TriggerWake {
		t.Errorf("trigger %v, want wake", tr.Trigger)
	}
}

// A dictation session hears everything itself (ADR-0045), so its file-choice
// question ("Which one?") opens no follow-up window: the pick is handled by
// the session, and nothing else listens.
func TestFollowUpYieldsToDictationChoice(t *testing.T) {
	five := []string{"/h/a/budget.txt", "/h/b/budget.md", "/h/c/budget 2025.txt", "/h/d/old budget.txt", "/h/e/budget.csv"}
	timers := &fakeTimers{}
	r := dictRig(t, func(d *core.Deps, c *core.Config) {
		d.Finder = &fakeFinder{found: five}
		c.FollowUp, c.After = true, timers.After
	})
	r.speaker().Live()
	r.focusOnOpen()
	f := &followRig{rig: r.rig, timers: timers}
	status := bus.Subscribe(r.bus, core.DictationChanged, 16)
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("Alfred open budget and write")
	f.start()
	await(t, status, func(s core.DictationStatus) bool { return s.Phase == core.PhaseChoosing })

	r.transcript("two")
	f.speak(2*time.Second, 3)
	deadline := time.Now().Add(guard)
	for len(r.launcher().Opened()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the pick was not handled by the dictation session")
		}
		time.Sleep(time.Millisecond)
	}
	if got := r.launcher().Opened(); got[0].Target != "/h/b/budget.md" {
		t.Errorf("opened %+v, want the second file", got)
	}
	if timers.count() != 0 || f.sawState(core.FollowUp) {
		t.Error("a follow-up window opened during a dictation session")
	}
}
