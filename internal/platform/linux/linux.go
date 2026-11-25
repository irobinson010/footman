//go:build linux

// Package linux is the Linux implementation of platform.Platform. Importing it
// registers it as "linux" in platform.Platforms.
//
// It is pure Go (no cgo): D-Bus through github.com/godbus/dbus/v5, plus the
// standard desktop tools xdg-open, gio, wl-copy and wl-paste for launching and
// the clipboard, and pactl, parec and pacat for audio. Input and Service return
// platform.ErrUnsupported until their own implementations land.
package linux

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/registry"
)

// Name is the registry name of this platform.
const Name = "linux"

func init() {
	platform.Platforms.Register(Name, func(cfg registry.Config) (platform.Platform, error) { return New(cfg) })
}

// Platform is the Linux platform.
type Platform struct {
	bus *bus

	input     unsupportedInput
	audio     *Audio
	service   unsupportedService
	windows   *Windows
	launcher  *Launcher
	notifier  *Notifier
	clipboard *Clipboard
	secrets   *Secrets
	paths     *Paths
}

var _ platform.Platform = (*Platform)(nil)

// New returns the Linux platform. It takes no options. D-Bus connections are
// opened lazily, so New works without a desktop session.
func New(cfg registry.Config) (*Platform, error) {
	if cfg != nil {
		var opts struct{}
		if err := cfg.Decode(&opts); err != nil {
			return nil, fmt.Errorf("linux platform: %w", err)
		}
	}
	return newPlatform(dbus.ConnectSessionBus, defaultRun), nil
}

func newPlatform(dial func(...dbus.ConnOption) (*dbus.Conn, error), run runFunc) *Platform {
	b := &bus{dial: dial}
	return &Platform{
		bus:       b,
		windows:   &Windows{bus: b, runtimeDir: runtimeDir},
		launcher:  &Launcher{run: run, dataDirs: dataDirs},
		notifier:  &Notifier{bus: b, waiters: map[uint32]*waiter{}},
		clipboard: &Clipboard{run: run},
		secrets:   &Secrets{bus: b},
		paths:     &Paths{},
		audio:     newAudio(run),
	}
}

// Input returns a stub that reports platform.ErrUnsupported.
func (p *Platform) Input() platform.Input { return p.input }

// Windows returns the KWin-backed window lookup.
func (p *Platform) Windows() platform.Windows { return p.windows }

// Launcher returns the xdg-open and gio launcher.
func (p *Platform) Launcher() platform.Launcher { return p.launcher }

// Notifier returns the org.freedesktop.Notifications notifier.
func (p *Platform) Notifier() platform.Notifier { return p.notifier }

// Clipboard returns the wl-copy and wl-paste clipboard.
func (p *Platform) Clipboard() platform.Clipboard { return p.clipboard }

// Audio returns the PulseAudio and PipeWire audio backend (pactl, parec and
// pacat).
func (p *Platform) Audio() platform.Audio { return p.audio }

// AudioDevices returns the audio backend with its Linux-only extras
// (Capturing and Watch).
func (p *Platform) AudioDevices() *Audio { return p.audio }

// Service returns a stub that reports platform.ErrUnsupported.
func (p *Platform) Service() platform.Service { return p.service }

// Secrets returns the Secret Service keychain.
func (p *Platform) Secrets() platform.Secrets { return p.secrets }

// Paths returns the XDG directories.
func (p *Platform) Paths() platform.Paths { return p.paths }

// Close closes the D-Bus connection, if one was opened.
func (p *Platform) Close() error { return p.bus.close() }

// bus lazily opens one shared session-bus connection.
type bus struct {
	dial func(...dbus.ConnOption) (*dbus.Conn, error)

	mu   sync.Mutex
	conn *dbus.Conn
}

func (b *bus) get() (*dbus.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil && b.conn.Connected() {
		return b.conn, nil
	}
	c, err := b.dial()
	if err != nil {
		return nil, fmt.Errorf("no D-Bus session bus: %w", err)
	}
	b.conn = c
	return c, nil
}

func (b *bus) close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	return err
}

// serviceMissing reports whether err means nothing owns the bus name.
func serviceMissing(err error) bool {
	var e dbus.Error
	if errors.As(err, &e) {
		switch e.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown",
			"org.freedesktop.DBus.Error.NameHasNoOwner",
			"org.freedesktop.DBus.Error.Spawn.ServiceNotFound":
			return true
		}
	}
	return false
}

type unsupportedInput struct{}

func (unsupportedInput) Hotkeys(context.Context, []string) (<-chan platform.HotkeyEvent, error) {
	return nil, fmt.Errorf("linux input: %w", platform.ErrUnsupported)
}
func (unsupportedInput) Type(context.Context, string) error {
	return fmt.Errorf("linux input: %w", platform.ErrUnsupported)
}
func (unsupportedInput) Paste(context.Context, string) error {
	return fmt.Errorf("linux input: %w", platform.ErrUnsupported)
}

type unsupportedService struct{}

var errService = fmt.Errorf("linux service: %w", platform.ErrUnsupported)

func (unsupportedService) Install(context.Context) error           { return errService }
func (unsupportedService) Uninstall(context.Context) error         { return errService }
func (unsupportedService) Installed(context.Context) (bool, error) { return false, errService }
