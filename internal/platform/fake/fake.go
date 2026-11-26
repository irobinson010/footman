// Package fake provides an in-memory platform for tests. It records what
// Footman does to the desktop (typed text, opened URLs, notifications, played
// audio) and lets a test script what the desktop does back (hotkey presses,
// windows, clipboard, microphone frames, notification clicks). Importing it
// registers the platform as "fake" in platform.Platforms:
//
//	p, _ := platform.Platforms.New("fake", nil)
//	_ = p.Launcher().OpenURL(ctx, "https://youtube.com")
//	p.Launcher().(*fake.Launcher).URLs() // ["https://youtube.com"]
//
// Each sub-interface is a separate exported type, reached by asserting the
// value its Platform method returns.
package fake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/registry"
)

// Name is the registry name of the fake platform.
const Name = "fake"

func init() {
	platform.Platforms.Register(Name, func(cfg registry.Config) (platform.Platform, error) { return New(cfg) })
}

// Platform is the fake platform. The zero value is not usable; use New.
type Platform struct {
	input     *Input
	windows   *Windows
	launcher  *Launcher
	notifier  *Notifier
	clipboard *Clipboard
	audio     *Audio
	service   *Service
	secrets   *Secrets
	paths     *Paths

	mu       sync.Mutex
	controls []string
	// ControlErr, if set, is returned by the volume and media methods.
	ControlErr error
	// Sys is what Facts and Status report (platform.SystemInfo); SysErr, if
	// set, is returned by both. StatusTop records Status's last top.
	Sys       platform.SystemFacts
	SysStatus platform.SystemStatus
	SysErr    error
	StatusTop int
}

// New builds a Platform from cfg (key "root": the directory under which Paths
// reports its directories; default footman-fake in the OS temp directory).
func New(cfg registry.Config) (*Platform, error) {
	opts := struct {
		Root string `json:"root"`
	}{Root: filepath.Join(os.TempDir(), "footman-fake")}
	if err := cfg.Decode(&opts); err != nil {
		return nil, err
	}
	return &Platform{
		input:     &Input{},
		windows:   &Windows{},
		launcher:  &Launcher{},
		notifier:  &Notifier{},
		clipboard: &Clipboard{},
		audio:     &Audio{},
		service:   &Service{},
		secrets:   &Secrets{m: map[string]string{}},
		paths:     &Paths{Root: opts.Root},
	}, nil
}

// Input implements platform.Platform; the value is an *Input.
func (p *Platform) Input() platform.Input { return p.input }

// Windows implements platform.Platform; the value is a *Windows.
func (p *Platform) Windows() platform.Windows { return p.windows }

// Launcher implements platform.Platform; the value is a *Launcher.
func (p *Platform) Launcher() platform.Launcher { return p.launcher }

// Notifier implements platform.Platform; the value is a *Notifier.
func (p *Platform) Notifier() platform.Notifier { return p.notifier }

// Clipboard implements platform.Platform; the value is a *Clipboard.
func (p *Platform) Clipboard() platform.Clipboard { return p.clipboard }

// Audio implements platform.Platform; the value is an *Audio.
func (p *Platform) Audio() platform.Audio { return p.audio }

// Service implements platform.Platform; the value is a *Service.
func (p *Platform) Service() platform.Service { return p.service }

// Secrets implements platform.Platform; the value is a *Secrets.
func (p *Platform) Secrets() platform.Secrets { return p.secrets }

// Paths implements platform.Platform; the value is a *Paths.
func (p *Platform) Paths() platform.Paths { return p.paths }

var _ platform.VolumeControl = (*Platform)(nil)
var _ platform.SystemInfo = (*Platform)(nil)

// Facts implements platform.SystemInfo with p.Sys.
func (p *Platform) Facts(context.Context) (platform.SystemFacts, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Sys, p.SysErr
}

// Status implements platform.SystemInfo with p.SysStatus.
func (p *Platform) Status(_ context.Context, top int) (platform.SystemStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.StatusTop = top
	return p.SysStatus, p.SysErr
}

var _ platform.MediaControl = (*Platform)(nil)

// ChangeVolume implements platform.VolumeControl.
func (p *Platform) ChangeVolume(_ context.Context, delta int) error {
	return p.record(fmt.Sprintf("volume %+d", delta))
}

// SetVolume implements platform.VolumeControl.
func (p *Platform) SetVolume(_ context.Context, percent int) error {
	return p.record(fmt.Sprintf("volume =%d", percent))
}

// SetMute implements platform.VolumeControl.
func (p *Platform) SetMute(_ context.Context, muted bool) error {
	if muted {
		return p.record("mute")
	}
	return p.record("unmute")
}

// Media implements platform.MediaControl.
func (p *Platform) Media(_ context.Context, cmd platform.MediaCommand) error {
	return p.record("media " + string(cmd))
}

func (p *Platform) record(s string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ControlErr != nil {
		return p.ControlErr
	}
	p.controls = append(p.controls, s)
	return nil
}

// Controls returns the volume and media calls so far, in order, such as
// "volume +10", "volume =40", "mute", "unmute" and "media pause".
func (p *Platform) Controls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.controls)
}
