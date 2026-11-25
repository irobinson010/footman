//go:build linux

package linux

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// startBus starts a private session bus and returns its address, or skips the
// test if dbus-daemon is not installed.
func startBus(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed; skipping the private-session-bus test")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "bus")
	// A minimal config instead of --session: the stock session config adds the
	// standard service directories, so on a desktop machine the bus would
	// auto-start the real notification daemon or keyring and the "no server"
	// tests would hang until the activation timeout.
	conf := filepath.Join(dir, "bus.conf")
	const busConf = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>%LISTEN%</listen>
  <keep_umask/>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`
	if err := os.WriteFile(conf, []byte(strings.Replace(busConf, "%LISTEN%", "unix:path="+sock, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "--config-file="+conf, "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Skipf("dbus-daemon printed no address: %v", err)
	}
	return strings.TrimSpace(line)
}

// newTestPlatform returns a platform wired to the private bus at addr.
func newTestPlatform(t *testing.T, addr string, run runFunc) *Platform {
	t.Helper()
	p := newPlatform(func(...dbus.ConnOption) (*dbus.Conn, error) { return dbus.Connect(addr) }, run)
	p.windows.runtimeDir = func() string { return t.TempDir() }
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// serve connects to addr, claims name and returns the connection.
func serve(t *testing.T, addr, name string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if name != "" {
		r, err := c.RequestName(name, dbus.NameFlagDoNotQueue)
		if err != nil || r != dbus.RequestNameReplyPrimaryOwner {
			t.Fatalf("RequestName(%s) = %v, %v", name, r, err)
		}
	}
	return c
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
