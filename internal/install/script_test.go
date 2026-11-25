package install_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInstallScript runs scripts/install.sh against a local "release" and a
// stub binary that records its arguments.
func TestInstallScript(t *testing.T) {
	for _, tool := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("script test targets linux amd64/arm64")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	script, err := filepath.Abs("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}

	stub := "#!/bin/sh\necho \"$@\" > \"$(dirname \"$0\")/args\"\n"
	release := t.TempDir()
	asset := "footman-linux-" + runtime.GOARCH
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(release, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(asset, stub, 0o644)
	write(asset+".sha256", fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(stub)), asset), 0o644)

	run := func(t *testing.T, dir string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+dir, "FOOTMAN_BASE_URL=file://"+release, "FOOTMAN_INSTALL_DIR="+filepath.Join(dir, "bin"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("installs and runs setup", func(t *testing.T) {
		dir := t.TempDir()
		out, err := run(t, dir, "--", "--non-interactive", "--name", "Ian")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		bin := filepath.Join(dir, "bin", "footman")
		if st, err := os.Stat(bin); err != nil || st.Mode()&0o111 == 0 {
			t.Errorf("binary not installed and executable: %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "bin", "args")); strings.TrimSpace(string(b)) != "setup --non-interactive --name Ian" {
			t.Errorf("setup args = %q", b)
		}
		// Uninstall mode hands off to footman uninstall.
		if out, err := run(t, dir, "--uninstall", "--dry-run"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "bin", "args")); strings.TrimSpace(string(b)) != "uninstall --dry-run" {
			t.Errorf("uninstall args = %q", b)
		}
	})

	t.Run("rejects a bad checksum", func(t *testing.T) {
		write(asset+".sha256", strings.Repeat("0", 64)+"  "+asset+"\n", 0o644)
		dir := t.TempDir()
		out, err := run(t, dir)
		if err == nil || !strings.Contains(out, "checksum mismatch") {
			t.Fatalf("err=%v out=%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "bin", "footman")); err == nil {
			t.Error("installed a binary that failed verification")
		}
	})
}
