//go:build unix

package api

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// shortDir returns a temporary directory with a path short enough for a Unix
// socket (about 100 bytes at most).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListen(t *testing.T) {
	path := filepath.Join(shortDir(t), "run", "footman.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v, want a socket with 0600", st.Mode())
	}
	if dst, _ := os.Stat(filepath.Dir(path)); dst.Mode().Perm() != 0o700 {
		t.Errorf("socket directory mode = %v, want 0700", dst.Mode().Perm())
	}

	// A second daemon must not take over a live socket.
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			_ = nc.Close()
		}
	}()
	if _, err := Listen(path); !errors.Is(err, ErrRunning) {
		t.Errorf("second Listen = %v, want ErrRunning", err)
	}

	_ = l.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket still there after Close: %v", err)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(shortDir(t), "footman.sock")
	// A socket file with nobody listening, as a crashed daemon leaves.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}

	l, err = Listen(path)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		nc, err := l.Accept()
		if err == nil {
			_ = nc.Close()
		}
	}()
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestListenRefusesSharedDirectory(t *testing.T) {
	dir := filepath.Join(shortDir(t), "shared")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(filepath.Join(dir, "footman.sock")); err == nil {
		_ = l.Close()
		t.Fatal("Listen accepted a world-writable directory")
	}
}
