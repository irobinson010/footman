package core_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
)

// gatedLLM streams its first sentence, then holds the rest of the reply back
// until the test lets it go.
type gatedLLM struct {
	first, rest string
	gate        chan struct{}
}

func (g gatedLLM) Capabilities() llm.Caps { return llm.Caps{Tools: true} }
func (g gatedLLM) Chat(ctx context.Context, _ llm.Request) (llm.Stream, error) {
	return &gatedStream{g: g, ctx: ctx}, nil
}

type gatedStream struct {
	g     gatedLLM
	ctx   context.Context
	state int
}

func (s *gatedStream) Close() error { return nil }
func (s *gatedStream) Recv() (llm.Event, error) {
	switch s.state {
	case 0:
		s.state = 1
		return llm.Event{Kind: llm.TextDelta, Text: s.g.first}, nil
	case 1:
		select {
		case <-s.g.gate:
		case <-s.ctx.Done():
			return llm.Event{}, s.ctx.Err()
		}
		s.state = 2
		return llm.Event{Kind: llm.TextDelta, Text: s.g.rest}, nil
	case 2:
		s.state = 3
		return llm.Event{Kind: llm.Done, Stop: llm.StopEnd}, nil
	}
	return llm.Event{}, io.EOF
}

// playerSpy tells the test when the first frame of a reply reaches the player.
type playerSpy struct {
	platform.Audio
	first chan struct{}
	once  sync.Once
}

func (p *playerSpy) Play(ctx context.Context, dev string, s audio.Stream) error {
	return p.Audio.Play(ctx, dev, spyStream{s, p})
}

type spyStream struct {
	audio.Stream
	p *playerSpy
}

func (s spyStream) Recv() (audio.Frame, error) {
	f, err := s.Stream.Recv()
	if err == nil && len(f.Samples) > 1 {
		s.p.once.Do(func() { close(s.p.first) })
	}
	return f, err
}

type spyPlatform struct {
	*pfake.Platform
	audio *playerSpy
}

func (p spyPlatform) Audio() platform.Audio { return p.audio }

// The first sentence reaches the player while the LLM is still streaming.
func TestFirstSentenceSpokenBeforeReplyEnds(t *testing.T) {
	spy := &playerSpy{first: make(chan struct{})}
	gate := make(chan struct{})
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		pl := d.Platform.(*pfake.Platform)
		spy.Audio = pl.Audio()
		d.Platform = spyPlatform{pl, spy}
		d.LLM = gatedLLM{first: "Good morning, sir. ", rest: "The weather is fine.", gate: gate}
		c.Ack = nil
	})
	starts := bus.Subscribe(r.bus, core.SpeechStarted, 4)
	done := make(chan error, 1)
	go func() {
		done <- r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "footman, hello"})
	}()

	select {
	case <-spy.first:
	case err := <-done:
		t.Fatalf("reply ended (%v) before its first sentence was played", err)
	case <-time.After(guard):
		t.Fatal("first sentence never reached the player")
	}
	equal(t, "spoken so far", spoken(r.tts), []string{"Good morning, sir."})
	select {
	case err := <-done:
		t.Fatalf("Submit returned early: %v", err)
	default:
	}
	if ev := await(t, starts, func(core.SpeechStart) bool { return true }); ev.After < 0 {
		t.Errorf("SpeechStart = %+v", ev)
	}

	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("Submit: %v", err)
	}
	equal(t, "spoken", spoken(r.tts), []string{"Good morning, sir.", "The weather is fine."})
	if played := r.speaker().Played(); len(played) != 1 {
		t.Errorf("played %d clips, want the reply as one", len(played))
	}
}

func TestReplyPolicy(t *testing.T) {
	at := func(h, m int) func() time.Time {
		return func() time.Time { return time.Date(2026, 11, 26, h, m, 0, 0, time.Local) }
	}
	tests := []struct {
		name     string
		speak    string
		hours    string
		now      func() time.Time
		typed    bool
		quiet    bool
		wantTTS  bool
		wantShow bool
	}{
		{"when-voice by voice", "when-voice", "", nil, false, false, true, false},
		{"default is when-voice", "", "", nil, false, false, true, false},
		{"when-voice typed", "when-voice", "", nil, true, false, false, true},
		{"always typed", "always", "", nil, true, false, true, false},
		{"never by voice", "never", "", nil, false, false, false, true},
		{"quiet mode", "always", "", nil, false, true, false, true},
		{"quiet hours, midnight crossing, late", "always", "22:00-07:00", at(23, 30), false, false, false, true},
		{"quiet hours, midnight crossing, early", "always", "22:00-07:00", at(3, 0), false, false, false, true},
		{"outside quiet hours", "always", "22:00-07:00", at(12, 0), false, false, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, func(_ *core.Deps, c *core.Config) {
				c.Speak, c.QuietHours, c.Now, c.Quiet = tc.speak, tc.hours, tc.now, tc.quiet
			})
			r.ai.Queue(lfake.Reply{Text: "Very good, sir."})
			replies := bus.Subscribe(r.bus, core.Replied, 4)
			if err := r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerCommand, Text: "hello", Typed: tc.typed}); err != nil {
				t.Fatal(err)
			}
			// Text replies are always published.
			if ev := await(t, replies, func(core.Reply) bool { return true }); ev.Text != "Very good, sir." {
				t.Errorf("Reply = %+v", ev)
			}
			if got := len(r.tts.Spoken()) > 0; got != tc.wantTTS {
				t.Errorf("spoken = %v, want %v", got, tc.wantTTS)
			}
			shown := r.plat.Notifier().(*pfake.Notifier).Shown()
			if got := len(shown) > 0; got != tc.wantShow {
				t.Errorf("notifications = %+v, want shown %v", shown, tc.wantShow)
			}
		})
	}
}

func TestSetReplyPolicyLive(t *testing.T) {
	r := newRig(t, nil)
	r.ai.Queue(lfake.Reply{Text: "One."}, lfake.Reply{Text: "Two."})
	r.submit(core.TriggerCommand, "a")
	r.p.SetReplyPolicy("never", "")
	r.submit(core.TriggerCommand, "b")
	equal(t, "spoken", spoken(r.tts), []string{"One."})
}

// A reply that is mostly code is shown, and a short line is spoken instead.
func TestCodeReplyIsShown(t *testing.T) {
	r := newRig(t, nil)
	code := "Here:\n```go\n" + "fmt.Println(\"a long line of code that is not worth reading aloud\")\n```\nOk."
	r.ai.Queue(lfake.Reply{Text: code})
	r.submit(core.TriggerCommand, "write code")
	shown := r.plat.Notifier().(*pfake.Notifier).Shown()
	if len(shown) != 1 || shown[0].Body != code {
		t.Errorf("notifications = %+v", shown)
	}
	equal(t, "spoken", spoken(r.tts), []string{"Here:", "Ok.", "That is mostly code, so I have put it on screen."})
}

// Stopping mid-reply cuts the speech: the rest of the reply is never spoken.
func TestStopMidReply(t *testing.T) {
	spy := &playerSpy{first: make(chan struct{})}
	gate := make(chan struct{})
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		pl := d.Platform.(*pfake.Platform)
		spy.Audio = pl.Audio()
		d.Platform = spyPlatform{pl, spy}
		d.LLM = gatedLLM{first: "Once upon a time. ", rest: "The end.", gate: gate}
		c.Ack = nil
	})
	done := make(chan error, 1)
	go func() {
		done <- r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "footman, tell a story"})
	}()
	<-spy.first
	r.p.Stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Submit = %v, want context.Canceled", err)
	}
	close(gate)
	equal(t, "spoken", spoken(r.tts), []string{"Once upon a time."})
	if got := r.p.State(); got != core.Idle {
		t.Errorf("state %v, want idle", got)
	}
}

// A TTS failure is reported, not swallowed.
func TestSpeakErrorIsReported(t *testing.T) {
	r := newRig(t, nil)
	r.tts.Err = errors.New("no voice")
	r.ai.Queue(lfake.Reply{Text: "Hello. There."})
	err := r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerCommand, Text: "hi"})
	if err == nil || !errors.Is(err, r.tts.Err) {
		t.Errorf("Submit = %v, want the TTS error", err)
	}
}
