package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrRunning is returned by Listen when another daemon already answers on the
// socket.
var ErrRunning = errors.New("api: a footman daemon is already running")

// Listen creates the daemon's Unix socket at path (design §29: user-only
// permissions). The socket is made 0600, and its parent directory is created
// with mode 0700 if missing; keep the socket in a directory only the user
// can enter (the runtime directory is one), so no one else can connect in
// the moment between creating the socket and the chmod; Listen refuses a
// directory other users can enter or that another user owns. A socket left
// behind by a daemon that died is replaced; one that still answers is not,
// and Listen returns ErrRunning.
//
// Closing the listener removes the socket file.
func Listen(path string) (net.Listener, error) {
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
