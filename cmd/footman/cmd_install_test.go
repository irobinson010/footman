package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
)

// installEnv points the commands at a fake platform inside a temp HOME.
func installEnv(t *testing.T, input string) (home string, p *fake.Platform) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	p, err := fake.New(registry.Map{"root": home})
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, "bin", "footman")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldIn, oldExe, oldPlat, oldSpeech := stdin, executable, newPlatform, speechStep
	t.Cleanup(func() { stdin, executable, newPlatform, speechStep = oldIn, oldExe, oldPlat, oldSpeech })
	// Tests never download models or touch the GPU (a -tags native build
	// would); the speech step has its own tests.
	speechStep = func(io.Writer) func(context.Context, install.SpeechEnv) error {
		return func(context.Context, install.SpeechEnv) error { return nil }
	}
	stdin = strings.NewReader(input)
	executable = func() (string, error) { return exe, nil }
	newPlatform = func() (platform.Platform, error) { return p, nil }
	return home, p
}

func runCmd(args ...string) (code int, out, errs string) {
	var o, e bytes.Buffer
	code = run(context.Background(), args, &o, &e)
	return code, o.String(), e.String()
}

func TestSetupThenUninstall(t *testing.T) {
	home, p := installEnv(t, "sk-test-key\n")

	code, out, errs := runCmd("setup", "--name", "Ian", "--provider", "claude", "--api-key-stdin")
	if code != 0 {
		t.Fatalf("setup = %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, "Setup complete") {
		t.Errorf("out = %q", out)
	}
	if v, err := p.Secrets().Get(context.Background(), "claude"); err != nil || v != "sk-test-key" {
		t.Errorf("secret = %q, %v", v, err)
	}
	if ok, _ := p.Service().Installed(context.Background()); !ok {
		t.Error("service not installed")
	}

	// Without a terminal and without a choice, uninstall refuses.
	if code, _, errs := runCmd("uninstall"); code != 2 || !strings.Contains(errs, "--purge") {
		t.Errorf("undecided uninstall = %d %q", code, errs)
	}
	// A dry run changes nothing.
	if code, out, _ := runCmd("uninstall", "--purge", "--dry-run"); code != 0 || !strings.Contains(out, "Dry run") {
		t.Errorf("dry run = %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "footman")); err != nil {
		t.Error("dry run removed the binary")
	}
	// Contradictory flags.
	if code, _, _ := runCmd("uninstall", "--purge", "--keep-data"); code != 2 {
		t.Errorf("purge+keep = %d", code)
	}

	code, out, errs = runCmd("uninstall", "--purge")
	if code != 0 {
		t.Fatalf("uninstall = %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("out = %q", out)
	}
	for _, gone := range []string{"bin/footman", "config", "data"} {
		if _, err := os.Stat(filepath.Join(home, gone)); err == nil {
			t.Errorf("%s still exists", gone)
		}
	}
	if _, err := p.Secrets().Get(context.Background(), "claude"); err == nil {
		t.Error("secret still in keychain")
	}

	// Nothing left to uninstall.
	if code, _, errs := runCmd("uninstall", "--purge"); code != 1 || !strings.Contains(errs, "nothing to uninstall") {
		t.Errorf("second uninstall = %d %q", code, errs)
	}
}

func TestSetupFlagErrors(t *testing.T) {
	installEnv(t, "")
	if code, _, _ := runCmd("setup", "--nope"); code != 2 {
		t.Errorf("bad flag = %d", code)
	}
	if code, _, errs := runCmd("setup", "--api-key-stdin"); code != 1 || !strings.Contains(errs, "no key") {
		t.Errorf("empty stdin = %d %q", code, errs)
	}
	if code, _, _ := runCmd("setup", "extra"); code != 2 {
		t.Errorf("extra arg = %d", code)
	}
}

func TestSetupSpeechStep(t *testing.T) {
	t.Run("runs by default and its lines print as they happen", func(t *testing.T) {
		installEnv(t, "")
		var ran bool
		speechStep = func(io.Writer) func(context.Context, install.SpeechEnv) error {
			return func(_ context.Context, se install.SpeechEnv) error {
				ran = true
				se.Step("speech       warming up")
				return nil
			}
		}
		code, out, errs := runCmd("setup", "--non-interactive")
		if code != 0 || !ran || strings.Count(out, "speech       warming up") != 1 {
			t.Fatalf("setup = %d ran=%v\n%s%s", code, ran, out, errs)
		}
	})
	t.Run("--skip-models skips it", func(t *testing.T) {
		installEnv(t, "")
		speechStep = func(io.Writer) func(context.Context, install.SpeechEnv) error {
			return func(context.Context, install.SpeechEnv) error { t.Error("speech step ran"); return nil }
		}
		code, out, errs := runCmd("setup", "--non-interactive", "--skip-models")
		if code != 0 || !strings.Contains(out, "skipped (--skip-models)") {
			t.Fatalf("setup = %d\n%s%s", code, out, errs)
		}
	})
	t.Run("a failed download fails setup and names the fix", func(t *testing.T) {
		_, p := installEnv(t, "")
		speechStep = func(io.Writer) func(context.Context, install.SpeechEnv) error {
			return func(context.Context, install.SpeechEnv) error { return io.ErrUnexpectedEOF }
		}
		code, _, errs := runCmd("setup", "--non-interactive")
		if code != 1 || !strings.Contains(errs, "speech models") {
			t.Fatalf("setup = %d %s", code, errs)
		}
		if ok, _ := p.Service().Installed(context.Background()); ok {
			t.Error("service installed with no models")
		}
	})
}
