//go:build linux && !integration

package linux

import (
	"errors"
	"testing"
)

// A plain unit test must not reach a bus it did not create (ADR-0031).
func TestGuardRefusesTheInheritedSession(t *testing.T) {
	addr := startBus(t) // reachable, but not ours to use through the inherited variable
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	old := inheritedBus
	t.Cleanup(func() { inheritedBus = old })

	// The test set the address itself: allowed.
	inheritedBus = "unix:path=/not/this/one"
	c, err := connectSessionBus()
	if err != nil {
		t.Fatalf("a bus the test set itself was refused: %v", err)
	}
	_ = c.Close()

	// The same address, but as if the process had inherited it: refused.
	inheritedBus = addr
	if c, err := connectSessionBus(); !errors.Is(err, errRealSession) {
		if c != nil {
			_ = c.Close()
		}
		t.Fatalf("inherited bus: err = %v, want errRealSession", err)
	}

	// The explicit opt-in lets it through.
	t.Setenv(optInEnv, "1")
	c, err = connectSessionBus()
	if err != nil {
		t.Fatalf("opt-in refused: %v", err)
	}
	_ = c.Close()
}

func TestGuardRefusesTheInheritedWaylandDisplay(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/test")
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	old := inheritedDisplay
	t.Cleanup(func() { inheritedDisplay = old })

	inheritedDisplay = "wayland-other"
	if got := waylandSocket(); got != "/run/user/test/wayland-test" {
		t.Errorf("display the test set: socket = %q", got)
	}
	inheritedDisplay = "wayland-test"
	if got := waylandSocket(); got != "" {
		t.Errorf("inherited display: socket = %q, want none", got)
	}
}

// New() uses the guarded dialer, so a platform made by a plain test cannot
// open the session bus the process inherited.
func TestNewPlatformIsGuarded(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", inheritedBus) // whatever the process started with
	p, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.bus.get(); !errors.Is(err, errRealSession) {
		t.Fatalf("bus.get() = %v, want errRealSession", err)
	}
}
