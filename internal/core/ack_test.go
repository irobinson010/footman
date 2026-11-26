package core_test

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/internal/persona/clipbank"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	sfake "github.com/irobinson010/footman/internal/speech/fake"
	"github.com/irobinson010/footman/personas"
)

func ackBank(t *testing.T) *clipbank.Bank {
	t.Helper()
	sub, err := fs.Sub(personas.FS, "ashford")
	if err != nil {
		t.Fatal(err)
	}
	pack, err := persona.Load(sub, "ashford")
	if err != nil {
		t.Fatal(err)
	}
	bank := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: must(sfake.NewTTS(registry.Map(nil)))})
	t.Cleanup(bank.Close)
	bank.Set(clipbank.Target{Pack: pack, Voice: speech.Voice{ID: "kokoro-en"}, Address: "sir"})
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	if err := bank.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	return bank
}

// A command plays a cached acknowledgement clip from the bank at once and
// reports how long after the end of speech it started.
func TestAckFromClipBank(t *testing.T) {
	bank := ackBank(t)
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.Ack = bank })
	acks := bus.Subscribe(r.bus, core.Acknowledged, 8)
	r.ai.Queue(lfake.Reply{Text: "Fine."})
	const spent = 250 * time.Millisecond // the speech ended this long ago
	u := core.Utterance{Trigger: core.TriggerCommand, Text: "how are you", Ended: time.Now().Add(-spent)}
	if err := r.p.Submit(r.ctx(), u); err != nil {
		t.Fatal(err)
	}
	a := await(t, acks, func(core.Acknowledgement) bool { return true })
	if a.Lead > 50*time.Millisecond {
		t.Errorf("ack started %v after the command did, want a few ms", a.Lead)
	}
	if a.Latency < spent || a.Latency > spent+100*time.Millisecond {
		t.Errorf("latency %v, want %v plus a few ms", a.Latency, spent)
	}
	if a.Clip <= 0 {
		t.Error("no clip length")
	}
	if n := len(r.speaker().Played()); n < 2 {
		t.Errorf("played %d clips, want the ack and the reply", n)
	}
}

func TestNoAckInQuietMode(t *testing.T) {
	bank := ackBank(t)
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.Ack = bank; c.Quiet = true })
	acks := bus.Subscribe(r.bus, core.Acknowledged, 8)
	r.ai.Queue(lfake.Reply{Text: "Fine."})
	r.submit(core.TriggerCommand, "how are you")
	select {
	case a := <-acks.C():
		t.Errorf("acknowledged in quiet mode: %+v", a)
	default:
	}
}
