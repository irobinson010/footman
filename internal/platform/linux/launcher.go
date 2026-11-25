//go:build linux

package linux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// Launcher opens URLs, apps and files with xdg-open and gio.
type Launcher struct {
	run      runFunc
	dataDirs func() []string
}

const launchGrace = 3 * time.Second

// OpenURL opens a URL with xdg-open. Only URLs with a scheme are accepted, and
// file:, javascript: and data: are refused.
func (l *Launcher) OpenURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("linux launcher: %q is not a URL with a scheme", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "file", "javascript", "data":
		return fmt.Errorf("linux launcher: refusing to open %s: URL", u.Scheme)
	}
	return l.xdgOpen(ctx, raw)
}

// OpenFile opens a file or folder with its default application.
func (l *Launcher) OpenFile(ctx context.Context, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("linux launcher: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("linux launcher: %s: %w", abs, platform.ErrNotFound)
		}
		return fmt.Errorf("linux launcher: %w", err)
	}
	return l.xdgOpen(ctx, abs)
}

func (l *Launcher) xdgOpen(ctx context.Context, target string) error {
	_, err := l.run(ctx, runSpec{Name: "xdg-open", Args: []string{"--", target}, Grace: launchGrace})
	if errors.Is(err, errNoTool) {
		return unsupportedTool("launcher", "install xdg-utils", err)
	}
	if err != nil {
		return fmt.Errorf("linux launcher: %w", err)
	}
	return nil
}

// OpenApp starts an application by desktop ID ("org.kde.konsole", with or
// without ".desktop"), by the name in its .desktop file ("Konsole"), or by a
// unique suffix of the desktop ID ("konsole").
func (l *Launcher) OpenApp(ctx context.Context, app string) error {
	app = strings.TrimSpace(app)
	if app == "" || strings.HasPrefix(app, "-") {
		return fmt.Errorf("linux launcher: invalid app %q", app)
	}
	file := l.findDesktopFile(app)
	if file == "" {
		return fmt.Errorf("linux launcher: app %q: %w", app, platform.ErrNotFound)
	}
	_, err := l.run(ctx, runSpec{Name: "gio", Args: []string{"launch", file}, Grace: launchGrace})
	if errors.Is(err, errNoTool) {
		id := strings.TrimSuffix(filepath.Base(file), ".desktop")
		_, err = l.run(ctx, runSpec{Name: "gtk-launch", Args: []string{id}, Grace: launchGrace})
		if errors.Is(err, errNoTool) {
			return unsupportedTool("launcher", "install glib2 (gio) or gtk-launch", err)
		}
	}
	if err != nil {
		return fmt.Errorf("linux launcher: %w", err)
	}
	return nil
}

// findDesktopFile returns the path of the .desktop file for app, or "".
func (l *Launcher) findDesktopFile(app string) string {
	id := strings.TrimSuffix(app, ".desktop")
	var suffix, byName string
	for _, d := range l.dataDirs() {
		dir := filepath.Join(d, "applications")
		exact := filepath.Join(dir, id+".desktop")
		if st, err := os.Stat(exact); err == nil && !st.IsDir() {
			return exact
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".desktop") {
				continue
			}
			base := strings.TrimSuffix(n, ".desktop")
			if suffix == "" && strings.EqualFold(filepath.Ext(base), "."+id) {
				suffix = filepath.Join(dir, n)
			}
			if byName == "" && strings.EqualFold(desktopName(filepath.Join(dir, n)), id) {
				byName = filepath.Join(dir, n)
			}
		}
	}
	if suffix != "" {
		return suffix
	}
	return byName
}

// desktopName reads the Name= key of the [Desktop Entry] group, skipping
// entries marked NoDisplay or Hidden.
func desktopName(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var name string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[Desktop Entry]"
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Name":
			name = strings.TrimSpace(v)
		case "NoDisplay", "Hidden":
			if strings.TrimSpace(v) == "true" {
				return ""
			}
		}
	}
	return name
}
