//go:build linux && !integration

package linux

import (
	"errors"
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
)

// The session guard (ADR-0031): a test binary built without the integration
// tag must never reach the real desktop. A plain unit test once opened a
// RemoteDesktop permission dialog on the owner's screen.

// optInEnv, set to "1", lets a test binary use the inherited session.
const optInEnv = "FOOTMAN_TEST_REAL_SESSION"

// The values the process started with. A test that starts a private bus or a
// nested compositor sets the variables itself, so they differ from these.
var (
	inheritedBus     = os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	inheritedDisplay = os.Getenv("WAYLAND_DISPLAY")
)

// errRealSession is returned instead of connecting to the real session.
var errRealSession = errors.New("refusing to reach the real desktop session from a test; start a private bus or nested compositor, or set " + optInEnv + "=1")

func guarding() bool { return testing.Testing() && os.Getenv(optInEnv) != "1" }

// connectSessionBus is dbus.ConnectSessionBus, refusing the inherited bus
// (or the default socket, when nothing is set) under test.
func connectSessionBus(opts ...dbus.ConnOption) (*dbus.Conn, error) {
	if guarding() && os.Getenv("DBUS_SESSION_BUS_ADDRESS") == inheritedBus {
		return nil, errRealSession
	}
	return dbus.ConnectSessionBus(opts...)
}

// realDisplayRefused reports whether the Wayland display must not be used.
func realDisplayRefused() bool {
	return guarding() && os.Getenv("WAYLAND_DISPLAY") == inheritedDisplay
}
