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
	"os"
	"path/filepath"

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
