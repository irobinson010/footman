//go:build linux && integration

package linux

import "github.com/godbus/dbus/v5"

// Integration builds are the ones allowed to use the real session.

func connectSessionBus(opts ...dbus.ConnOption) (*dbus.Conn, error) {
	return dbus.ConnectSessionBus(opts...)
}

func realDisplayRefused() bool { return false }
