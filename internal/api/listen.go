package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ErrRunning is returned by Listen when another daemon already answers on the
// socket.
var ErrRunning = errors.New("api: a footman daemon is already running")

// maxSocketPath is the longest socket path the OS accepts, in bytes. The
// kernel's sun_path holds 108 bytes on Linux and 104 on macOS and the BSDs,
// and the path must leave room for its terminating NUL.
func maxSocketPath() int {
	switch runtime.GOOS {
	case "darwin", "ios", "freebsd", "netbsd", "openbsd", "dragonfly":
		return 103
	}
	return 107
}

// CheckSocketPath returns an error, naming the length and the limit, when path
// is too long to be a Unix socket address on this system. Such a path makes
// bind fail with an unhelpful "invalid argument".
func CheckSocketPath(path string) error {
	return checkSocketPath(path, maxSocketPath())
}

func checkSocketPath(path string, limit int) error {
	if n := len(path); n > limit {
		return fmt.Errorf("socket path too long (%d bytes, max %d): use a shorter -socket or XDG_RUNTIME_DIR", n, limit)
	}
	return nil
}

// Listen creates the daemon's Unix socket at path (design §29: user-only
// permissions). The socket is made 0600, and its parent directory is created
// with mode 0700 if missing; keep the socket in a directory only the user
// can enter (the runtime directory is one), so no one else can connect in
// the moment between creating the socket and the chmod; Listen refuses a
// directory other users can enter or that another user owns. A socket left
// behind by a daemon that died is replaced; one that still answers is not,
// and Listen returns ErrRunning. A path too long for the OS's socket address
// is refused before anything is created (see CheckSocketPath).
//
// Closing the listener removes the socket file.
func Listen(path string) (net.Listener, error) {
	if err := CheckSocketPath(path); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("api: socket directory: %w", err)
	}
	if err := checkDir(dir); err != nil {
		return nil, fmt.Errorf("api: unsafe socket directory: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var d net.Dialer
		nc, derr := d.DialContext(ctx, "unix", path)
		cancel()
		if derr == nil {
			_ = nc.Close()
			return nil, fmt.Errorf("%w (socket %s)", ErrRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("api: remove stale socket: %w", err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("api: listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("api: socket permissions: %w", err)
	}
	return l, nil
}
