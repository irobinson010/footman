//go:build linux

package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/platform"
)

// testService is a Service writing to a temporary unit folder, with systemctl
// replaced by a recorder: the real user manager is never touched.
func testService(t *testing.T, err error) (*Service, *[]runSpec) {
	t.Helper()
	run, calls := recorder(runResult{}, err)
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin dir", "footman")
	s := &Service{
		run:        run,
		unitDir:    func() string { return filepath.Join(dir, "systemd", "user") },
		executable: func() (string, error) { return exe, nil },
	}
	return s, calls
}

func commands(calls []runSpec) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

func TestServiceInstallUninstall(t *testing.T) {
	s, calls := testService(t, nil)
	ctx := context.Background()
	if ok, err := s.Installed(ctx); ok || err != nil {
		t.Fatalf("Installed before install = %v, %v", ok, err)
	}
	if err := s.Install(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.UnitPath())
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := s.executable()
	for _, want := range []string{
		`ExecStart="` + exe + `" daemon`,
		"WantedBy=graphical-session.target",
		"PartOf=graphical-session.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("unit lacks %q:\n%s", want, b)
		}
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable footman.service",
		"systemctl --user restart footman.service",
	}
	if got := commands(*calls); !slices.Equal(got, want) {
		t.Errorf("install ran %q, want %q", got, want)
	}
	if ok, err := s.Installed(ctx); !ok || err != nil {
		t.Errorf("Installed after install = %v, %v", ok, err)
	}

	*calls = nil
	if err := s.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	want = []string{"systemctl --user disable --now footman.service", "systemctl --user daemon-reload"}
	if got := commands(*calls); !slices.Equal(got, want) {
		t.Errorf("uninstall ran %q, want %q", got, want)
	}
	if _, err := os.Stat(s.UnitPath()); !os.IsNotExist(err) {
		t.Errorf("unit left behind: %v", err)
	}
	// Idempotent: nothing to do, nothing run.
	*calls = nil
	if err := s.Uninstall(ctx); err != nil || len(*calls) != 0 {
		t.Errorf("second uninstall = %v, ran %q", err, commands(*calls))
	}
}

func TestServiceWithoutSystemd(t *testing.T) {
	s, _ := testService(t, errNoTool)
	err := s.Install(context.Background())
	if !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Install without systemctl = %v, want ErrUnsupported", err)
	}
}

// A failing disable still removes the unit file.
func TestServiceUninstallRemovesUnitOnError(t *testing.T) {
	s, _ := testService(t, nil)
	if err := s.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.run, _ = recorder(runResult{}, errors.New("Failed to disable unit"))
	if err := s.Uninstall(context.Background()); err == nil {
		t.Error("Uninstall hid the systemctl error")
	}
	if _, err := os.Stat(s.UnitPath()); !os.IsNotExist(err) {
		t.Errorf("unit left behind: %v", err)
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/footman":    "/usr/bin/footman",
		"/opt/my apps/fm":     `"/opt/my apps/fm"`,
		"/home/u/100%/fm":     `"/home/u/100%%/fm"`,
		`/x/"quoted"/$HOME`:   `"/x/\"quoted\"/$$HOME"`,
		`/back\slash/footman`: `"/back\\slash/footman"`,
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
