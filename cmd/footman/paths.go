package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// userPaths are the per-user locations from design §10.
type userPaths struct {
	// Config holds config.toml.
	Config string
	// Data holds footman.db.
	Data string
	// Runtime holds footman.sock.
	Runtime string
}

// socketEnv overrides the socket path for the daemon and every client.
const socketEnv = "FOOTMAN_SOCKET"

// defaultPaths returns the default locations. On Linux and the BSDs they
// follow the XDG base directories (~/.config/footman, ~/.local/share/footman
// and $XDG_RUNTIME_DIR, or /tmp/footman-<uid> when it is unset), matching
// the Linux platform's Paths. Elsewhere config and data go in the OS
// config directory and the socket in a per-user directory under the temp
// directory, until those platforms get native paths (named pipes on
// Windows).
//
// The client commands and the daemon both use this, so they agree on the
// socket without the client having to load a platform (ADR-0025).
func defaultPaths() userPaths {
	home, _ := os.UserHomeDir()
	env := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" && filepath.IsAbs(v) {
			return v
		}
		return fallback
	}
	tmpRuntime := filepath.Join(os.TempDir(), "footman-"+strconv.Itoa(os.Getuid()))
	switch runtime.GOOS {
	case "windows", "darwin", "ios", "android", "plan9", "js", "wasip1":
		dir, err := os.UserConfigDir()
		if err != nil {
			dir = filepath.Join(home, ".footman")
		}
		dir = filepath.Join(dir, "footman")
		return userPaths{Config: dir, Data: dir, Runtime: tmpRuntime}
	}
	p := userPaths{
		Config:  filepath.Join(env("XDG_CONFIG_HOME", filepath.Join(home, ".config")), "footman"),
		Data:    filepath.Join(env("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "footman"),
		Runtime: tmpRuntime,
	}
	if rt := env("XDG_RUNTIME_DIR", ""); rt != "" {
		p.Runtime = rt
	}
	return p
}

// defaultSocket returns $FOOTMAN_SOCKET, or <runtime>/footman.sock.
func defaultSocket() string {
	if s := os.Getenv(socketEnv); s != "" {
		return s
	}
	return filepath.Join(defaultPaths().Runtime, "footman.sock")
}
