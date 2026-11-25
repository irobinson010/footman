//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/irobinson010/footman/internal/platform"
)

// UnitName is the systemd user unit that runs `footman daemon`.
const UnitName = "footman.service"

// Service installs Footman as a systemd user service (ADR-0030). The unit
// lives in $XDG_CONFIG_HOME/systemd/user and is started with the graphical
// session, so the daemon gets the session's Wayland display, D-Bus bus and
// audio server from the user manager's environment.
type Service struct {
	run runFunc
	// unitDir returns the user unit folder.
	unitDir func() string
	// executable returns the program the unit runs.
	executable func() (string, error)
}

var _ platform.Service = (*Service)(nil)

func newService(run runFunc) *Service {
	return &Service{
		run:        run,
		unitDir:    func() string { return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "systemd", "user") },
		executable: os.Executable,
	}
}

// UnitPath is where the unit file is installed.
func (s *Service) UnitPath() string { return filepath.Join(s.unitDir(), UnitName) }

// unit is the unit file for binary.
func unit(binary string) string {
	return `# Installed by footman setup; removed by footman uninstall.
[Unit]
Description=Footman voice dictation and assistant
Documentation=https://github.com/irobinson010/footman
PartOf=graphical-session.target
After=graphical-session.target pipewire-pulse.service

[Service]
Type=simple
ExecStart=` + systemdQuote(binary) + ` daemon
# Fewer malloc arenas: less memory kept after the speech models unload.
Environment=MALLOC_ARENA_MAX=2
Restart=on-failure
RestartSec=3

[Install]
WantedBy=graphical-session.target
`
}

// systemdQuote quotes a path for an ExecStart line when needed.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\%$;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

func (s *Service) systemctl(ctx context.Context, args ...string) error {
	_, err := s.run(ctx, runSpec{Name: "systemctl", Args: append([]string{"--user"}, args...), Capture: true})
	if errors.Is(err, errNoTool) {
		return unsupportedTool("service", "Footman's background service needs systemd", err)
	}
	return err
}

// Install writes the unit for the running program, enables it for the
// graphical session and (re)starts it, so a new binary or config takes
// effect at once. It is idempotent.
func (s *Service) Install(ctx context.Context) error {
	exe, err := s.executable()
	if err != nil {
		return fmt.Errorf("linux service: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := os.MkdirAll(s.unitDir(), 0o755); err != nil {
		return fmt.Errorf("linux service: %w", err)
	}
	path := s.UnitPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(unit(exe)), 0o644); err != nil {
		return fmt.Errorf("linux service: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("linux service: %w", err)
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", UnitName}, {"restart", UnitName}} {
		if err := s.systemctl(ctx, args...); err != nil {
			return fmt.Errorf("linux service: systemctl --user %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// Uninstall stops and disables the service and removes the unit. It is
// idempotent: a service that is not there is not an error.
func (s *Service) Uninstall(ctx context.Context) error {
	path := s.UnitPath()
	_, statErr := os.Stat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	// Stop it even if disabling fails, and remove the file either way.
	err := s.systemctl(ctx, "disable", "--now", UnitName)
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		err = errors.Join(err, rerr)
	}
	if derr := s.systemctl(ctx, "daemon-reload"); derr != nil && err == nil {
		err = derr
	}
	if err != nil {
		return fmt.Errorf("linux service: %w", err)
	}
	return nil
}

// Installed reports whether the unit file is installed.
func (s *Service) Installed(context.Context) (bool, error) {
	_, err := os.Stat(s.UnitPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
