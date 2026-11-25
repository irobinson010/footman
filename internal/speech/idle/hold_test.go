package idle

import (
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/speech"
)

// Hold (push-to-talk) reloads an unloaded engine without blocking, and keeps
// it loaded however long the user speaks, until released.
func TestSTTHoldLoadsAndKeepsLoaded(t *testing.T) {
	h, f, c := setup(t, time.Minute)
	c.advance(time.Minute)
	if h.State().Loaded {
		t.Fatal("not unloaded after the idle time")
	}
	release := STT{H: h}.Hold()
	deadline := time.Now().Add(5 * time.Second)
	for !h.State().Loaded || h.State().InUse != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("Hold did not load and hold the engine: %+v", h.State())
		}
		time.Sleep(time.Millisecond)
	}
	c.advance(10 * time.Minute) // a long utterance
	if !h.State().Loaded {
		t.Fatal("unloaded while held")
	}
	release()
	release() // twice is harmless
	for h.State().InUse != 0 {
		if time.Now().After(deadline) {
			t.Fatal("Hold's release did not release")
		}
		time.Sleep(time.Millisecond)
	}
	c.advance(time.Minute)
	if h.State().Loaded || len(f.built) != 2 {
		t.Errorf("after release and the idle time: %+v, %d builds", h.State(), len(f.built))
	}
}

// OnUnload runs after an idle unload, such as to give memory back.
func TestOnUnloadRuns(t *testing.T) {
	c := newClock()
	f := &factory{}
	first, _ := f.load()
	ran := make(chan struct{}, 1)
	h := New[speech.STT]("stt", "fake", first, f.load, Options{
		After: func() time.Duration { return time.Second }, AfterFunc: c.AfterFunc, Now: c.Now,
		OnUnload: func() { ran <- struct{}{} },
	})
	c.advance(time.Second)
	if h.State().Loaded {
		t.Fatal("not unloaded")
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("OnUnload did not run")
	}
}
