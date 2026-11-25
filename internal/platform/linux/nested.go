//go:build linux && integration

package linux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// This file is built only with the integration tag. It runs a private,
// headless KWin session (testdata/nested-kwin.sh) for the nested input tests
// in this package and the end-to-end test in cmd/footman: its own Wayland
// socket, D-Bus session bus, portals, kglobalaccel, permission store and XDG
// folders. Nothing reaches the real desktop: everything connects only to the
// session's addresses, never to $WAYLAND_DISPLAY or the session bus. See
// README.md ("Nested input tests").

// Nested is a running nested KWin session.
type Nested struct {
	// Root is the session's folder; everything it writes is under it.
	Root string
	// Env is the session's environment: DBUS_SESSION_BUS_ADDRESS,
	// WAYLAND_DISPLAY, XDG_RUNTIME_DIR, the XDG_* folders and so on.
	Env map[string]string

	cmd      *exec.Cmd
	attached bool
}

// NestedTestdata returns the path of a file in this package's testdata
// folder, such as "qtsink.py".
func NestedTestdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

// NestedTools are the programs a nested session needs.
var NestedTools = []string{"kwin_wayland", "dbus-run-session", "dbus-update-activation-environment"}

// StartNested starts a nested session in a new temporary folder, or attaches
// to one started by hand when $FOOTMAN_NESTED_ROOT names its folder (it is
// then left running).
func StartNested() (*Nested, error) {
	if root := os.Getenv("FOOTMAN_NESTED_ROOT"); root != "" {
		n := &Nested{Root: root, attached: true}
		return n, n.readEnv()
	}
	root, err := os.MkdirTemp("", "fmnest")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(NestedTestdata("nested-kwin.sh"), root)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logf, err := os.Create(filepath.Join(root, "session.log"))
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	// The script clears the session variables itself; this keeps the real
	// desktop's out of even the outer shell.
	cmd.Env = cleanEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	n := &Nested{Root: root, cmd: cmd}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if n.readEnv() == nil {
			// Give the portal backends a moment to come up.
			time.Sleep(time.Second)
			return n, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	n.Stop()
	log, _ := os.ReadFile(filepath.Join(root, "session.log"))
	return nil, fmt.Errorf("nested KWin did not start:\n%s", log)
}

// cleanEnv drops the variables that lead to the real desktop session.
func cleanEnv(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "DBUS_SESSION_BUS_ADDRESS", "WAYLAND_DISPLAY", "DISPLAY", "SESSION_MANAGER", "XDG_SESSION_ID":
			return true
		}
		return false
	})
}

func (n *Nested) readEnv() error {
	b, err := os.ReadFile(filepath.Join(n.Root, "env"))
	if err != nil {
		return err
	}
	n.Env = map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			n.Env[k] = v
		}
	}
	return nil
}

// Stop ends the session and removes its folder (unless
// $FOOTMAN_NESTED_KEEP is set). An attached session is left alone.
func (n *Nested) Stop() {
	if n.attached {
		return
	}
	// Stop the session's services before KWin: a portal backend that loses
	// its compositor aborts, and the crash report would reach the desktop.
	killNestedLeftovers(filepath.Join(n.Root, "run"), "kwin_wayland", "dbus-daemon", "dbus-run-sessio")
	time.Sleep(500 * time.Millisecond)
	if n.cmd != nil && n.cmd.Process != nil {
		_ = syscall.Kill(-n.cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = n.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-n.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	// Services the private bus started (portals, the document portal's
	// FUSE mount) can outlive it: stop everything whose runtime folder is
	// the nested one.
	killNestedLeftovers(filepath.Join(n.Root, "run"))
	for _, fu := range []string{"fusermount3", "fusermount"} {
		if exec.Command(fu, "-u", "-z", filepath.Join(n.Root, "run", "doc")).Run() == nil {
			break
		}
	}
	if os.Getenv("FOOTMAN_NESTED_KEEP") == "" {
		// KWin may still flush its config while exiting.
		for range 3 {
			time.Sleep(300 * time.Millisecond)
			_ = os.RemoveAll(n.Root)
		}
	}
}

// killNestedLeftovers stops this user's processes whose XDG_RUNTIME_DIR is
// runDir, except those named in keep. No other process can match: runDir is
// a fresh temporary folder.
func killNestedLeftovers(runDir string, keep ...string) {
	want := []byte("XDG_RUNTIME_DIR=" + runDir + "\x00")
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil || !bytes.Contains(env, want) {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if slices.Contains(keep, strings.TrimSpace(string(comm))) {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

// Environ is the environment for a program run inside the session: HOME and
// PATH from this process, LANG=C.UTF-8, the session's variables, then
// extra (KEY=VALUE, overriding).
func (n *Nested) Environ(extra ...string) []string {
	out := []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	for k, v := range n.Env {
		out = append(out, k+"="+v)
	}
	return append(out, extra...)
}

// Dial connects to the session's D-Bus bus.
func (n *Nested) Dial(...dbus.ConnOption) (*dbus.Conn, error) {
	return dbus.Connect(n.Env["DBUS_SESSION_BUS_ADDRESS"])
}

// NewPlatform returns a Platform wired only to the session. Its RemoteDesktop
// restore token is kept in tokenFile.
func (n *Nested) NewPlatform(tokenFile string) *Platform {
	p := newPlatform(n.Dial, defaultRun)
	p.windows.runtimeDir = func() string { return n.Env["XDG_RUNTIME_DIR"] }
	in := p.input
	in.socket = func() string { return filepath.Join(n.Env["XDG_RUNTIME_DIR"], n.Env["WAYLAND_DISPLAY"]) }
	in.dataHome = func() string { return n.Env["XDG_DATA_HOME"] }
	in.tokenFile = tokenFile
	in.remote.tokenFile = tokenFile
	return p
}

// AllowRemoteDesktop pre-approves the RemoteDesktop dialog for Footman in
// the session's permission store (KDE's "kde-authorized" table), since no
// one is there to click it.
func (n *Nested) AllowRemoteDesktop() error {
	c, err := n.Dial()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Object("org.freedesktop.impl.portal.PermissionStore", "/org/freedesktop/impl/portal/PermissionStore").
		Call("org.freedesktop.impl.portal.PermissionStore.SetPermission", 0, "kde-authorized", true, "remote-desktop", AppID, []string{"yes"}).Err
}

// Keysyms the tests press.
const (
	KeysymReturn = keysymReturn
	KeysymF8     = 0xffc5
)

// Key presses (pressed) or releases sym through the platform's RemoteDesktop
// portal session, the path a physical key takes into KWin's shortcut
// handling.
func (p *Platform) Key(ctx context.Context, sym uint32, pressed bool) error {
	pc, s, err := p.input.remote.ensure(ctx)
	if err != nil {
		return err
	}
	state := keyReleased
	if pressed {
		state = keyPressed
	}
	return p.input.remote.keysym(ctx, pc, s, sym, state)
}

// ApproveShortcutDialogs answers KDE's "Global Shortcuts Requested" dialog by
// pressing Return in it whenever it has focus, until ctx is done. The
// returned channel is closed when it has stopped.
func (p *Platform) ApproveShortcutDialogs(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			wctx, c := context.WithTimeout(ctx, time.Second)
			w, err := p.Windows().Active(wctx)
			c()
			if err == nil && (strings.Contains(w.App, "portal.desktop.kde") || strings.Contains(w.Title, "Global Shortcuts")) {
				time.Sleep(300 * time.Millisecond)
				_ = p.Key(ctx, keysymReturn, true)
				_ = p.Key(ctx, keysymReturn, false)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	return done
}
