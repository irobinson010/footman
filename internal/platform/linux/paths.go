//go:build linux

package linux

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths returns the XDG directories (design §10).
type Paths struct{}

var _ interface {
	Config() string
	Data() string
	Cache() string
	Runtime() string
} = (*Paths)(nil)

const appDir = "footman"

// xdgDir returns the absolute directory in env, or ~/fallback. Per the XDG
// spec, relative values are invalid and ignored.
func xdgDir(env, fallback string) string {
	if v := os.Getenv(env); filepath.IsAbs(v) {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		home = filepath.Join(os.TempDir(), fmt.Sprintf("footman-home-%d", os.Getuid()))
	}
	return filepath.Join(home, fallback)
}

// Config is $XDG_CONFIG_HOME/footman (default ~/.config/footman).
func (*Paths) Config() string { return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), appDir) }

// Data is $XDG_DATA_HOME/footman (default ~/.local/share/footman).
func (*Paths) Data() string { return filepath.Join(xdgDir("XDG_DATA_HOME", ".local/share"), appDir) }

// Cache is $XDG_CACHE_HOME/footman (default ~/.cache/footman).
func (*Paths) Cache() string { return filepath.Join(xdgDir("XDG_CACHE_HOME", ".cache"), appDir) }

// Runtime is $XDG_RUNTIME_DIR, which systemd makes private to the user (mode
// 0700). If it is unset, it is /tmp/footman-<uid>, which the caller must create
// with mode 0700.
func (*Paths) Runtime() string { return runtimeDir() }

func runtimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("footman-%d", os.Getuid()))
}

// dataDirs returns the application search path: $XDG_DATA_HOME then
// $XDG_DATA_DIRS.
func dataDirs() []string {
	dirs := []string{xdgDir("XDG_DATA_HOME", ".local/share")}
	d := os.Getenv("XDG_DATA_DIRS")
	if d == "" {
		d = "/usr/local/share:/usr/share"
	}
	for _, x := range filepath.SplitList(d) {
		if filepath.IsAbs(x) {
			dirs = append(dirs, x)
		}
	}
	return dirs
}
