package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
)

// noHotkeys is a platform whose input cannot register hotkeys, as on a
// session without input access.
type noHotkeys struct{ platform.Platform }

func (p noHotkeys) Input() platform.Input { return noHotkeysInput{p.Platform.Input()} }

type noHotkeysInput struct{ platform.Input }

func (noHotkeysInput) Hotkeys(context.Context, []string) (<-chan platform.HotkeyEvent, error) {
	return nil, errors.New("linux input: platform: not supported here")
}

func TestMicStatusIsOffUntilRun(t *testing.T) {
	r := newRig(t, nil)
	if got := r.p.Mic(); got != (core.MicStatus{State: core.MicOff}) {
		t.Errorf("Mic before Run = %+v, want off", got)
	}
}

func TestMicStatusReportsTheErrorThatStoppedIt(t *testing.T) {
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.Platform = noHotkeys{d.Platform} })
	const want = "linux input: platform: not supported here"
	if err := r.p.Run(r.ctx()); err == nil || err.Error() != want {
		t.Fatalf("Run = %v, want %q", err, want)
	}
	if got := r.p.Mic(); got != (core.MicStatus{State: core.MicStopped, Error: want}) {
		t.Errorf("Mic after the loop failed = %+v", got)
	}
}

func TestMicStatusWhenTheStreamEnds(t *testing.T) {
	// Nothing is queued on the fake microphone, so its stream ends at once,
	// as it does when the device goes away.
	r := newRig(t, nil)
	if err := r.p.Run(r.ctx()); err != nil {
		t.Fatalf("Run = %v, want nil when the stream ends on its own", err)
	}
	want := core.MicStatus{State: core.MicStopped, Error: "the microphone stream ended"}
	if got := r.p.Mic(); got != want {
		t.Errorf("Mic after the stream ended = %+v, want %+v", got, want)
	}
}
