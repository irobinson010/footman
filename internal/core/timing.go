package core

import (
	"context"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/bus"
)

// TurnTiming is where the time of one spoken command went, published on
// Timing when a command turn ends. It carries no text, so it can be logged.
// Every duration is zero when that step did not happen (no AI token for a
// fast-path command, no audio for a reply that is only shown).
type TurnTiming struct {
	Turn    uint64
	Trigger Trigger
	// WakeToEnd is from the wake word being heard to the end of speech (the
	// VAD handing over the utterance, which includes its min_silence). Only
	// for wake-word turns.
	WakeToEnd time.Duration
	// Transcribe is how long speech-to-text took.
	Transcribe time.Duration
	// Ack, FirstToken and FirstAudio are measured from the end of speech: to
	// the first sample of the acknowledgement clip, the first text of the
	// AI's reply, and the first sample of the spoken reply.
	Ack, FirstToken, FirstAudio time.Duration
}

// WakeToAudio is from the wake word being heard to the first spoken audio of
// the reply, or zero if either is unknown.
func (t TurnTiming) WakeToAudio() time.Duration {
	if t.WakeToEnd <= 0 || t.FirstAudio <= 0 {
		return 0
	}
	return t.WakeToEnd + t.FirstAudio
}

// Timing reports where the time of each spoken command went.
var Timing = bus.NewTopic[TurnTiming]("pipeline.timing")

// turnClock collects the moments of one turn's steps. It travels in the
// turn's context, since the steps run on different goroutines.
type turnClock struct {
	mu                sync.Mutex
	wakeAt, ended     time.Time
	transcribe        time.Duration
	ack, token, audio time.Time
}

type clockKey struct{}

func withClock(ctx context.Context, c *turnClock) context.Context {
	return context.WithValue(ctx, clockKey{}, c)
}

func clockFrom(ctx context.Context) *turnClock {
	c, _ := ctx.Value(clockKey{}).(*turnClock)
	return c
}

// markStep records the first time a step happens; pick selects its field.
func markStep(ctx context.Context, pick func(*turnClock) *time.Time) {
	c := clockFrom(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := pick(c); t.IsZero() {
		*t = time.Now()
	}
}

func markAck(ctx context.Context)   { markStep(ctx, func(c *turnClock) *time.Time { return &c.ack }) }
func markToken(ctx context.Context) { markStep(ctx, func(c *turnClock) *time.Time { return &c.token }) }
func markAudio(ctx context.Context) { markStep(ctx, func(c *turnClock) *time.Time { return &c.audio }) }

// timing is the TurnTiming of the turn so far, or false when the turn did not
// come from speech (typed text has no end of speech to count from).
func (c *turnClock) timing(id uint64, trig Trigger) (TurnTiming, bool) {
	if c == nil {
		return TurnTiming{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended.IsZero() {
		return TurnTiming{}, false
	}
	since := func(t time.Time) time.Duration {
		if t.IsZero() {
			return 0
		}
		return max(t.Sub(c.ended), 0)
	}
	t := TurnTiming{Turn: id, Trigger: trig, Transcribe: c.transcribe,
		Ack: since(c.ack), FirstToken: since(c.token), FirstAudio: since(c.audio)}
	if !c.wakeAt.IsZero() {
		t.WakeToEnd = max(c.ended.Sub(c.wakeAt), 0)
	}
	return t, true
}
