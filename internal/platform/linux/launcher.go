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

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// Launcher opens URLs, apps and files with xdg-open and gio.
type Launcher struct {
	bus      *bus // for the OpenURI portal fallback; may be nil
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
		if perr := l.portalOpen(ctx, target); perr == nil {
			return nil
		}
		return unsupportedTool("launcher", "install xdg-utils", err)
	}
	if err != nil {
		return fmt.Errorf("linux launcher: %w", err)
	}
	return nil
}

// portalOpen opens target through the OpenURI portal, for desktops without
// xdg-open.
func (l *Launcher) portalOpen(ctx context.Context, target string) error {
	if l.bus == nil {
		return errors.New("no bus")
	}
	conn, err := l.bus.get()
	if err != nil {
		return err
	}
	uri := target
	if strings.HasPrefix(target, "/") {
		uri = "file://" + target
	}
	return conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop").
		CallWithContext(ctx, "org.freedesktop.portal.OpenURI.OpenURI", 0, "", uri, map[string]dbus.Variant{}).Err
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

// findDesktopFile returns the path of the .desktop file for app, or "". An
// exact desktop ID wins, then an ID suffix, then the best fuzzy match on the
// entry's Name, GenericName and Keywords.
func (l *Launcher) findDesktopFile(app string) string {
	id := strings.TrimSuffix(app, ".desktop")
	var suffix, best string
	bestScore := 0
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
			path := filepath.Join(dir, n)
			base := strings.TrimSuffix(n, ".desktop")
			if suffix == "" && strings.EqualFold(filepath.Ext(base), "."+id) {
				suffix = path
			}
			if s := fuzzyScore(id, readDesktop(path)); s > bestScore {
				best, bestScore = path, s
			}
		}
	}
	if suffix != "" {
		return suffix
	}
	return best
}

// fuzzyScore rates how well query names an app with the given labels (Name,
// GenericName, Keywords): 4 for an exact match, 3 for a prefix, 2 for a
// substring of 3 or more letters, 1 for a typo of one letter in a word (speech
// to text often mishears), 0 for no match.
func fuzzyScore(query string, labels []string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	best := 0
	for _, label := range labels {
		l := strings.ToLower(label)
		s := 0
		switch {
		case l == q:
			s = 4
		case strings.HasPrefix(l, q):
			s = 3
		case len(q) >= 3 && strings.Contains(l, q):
			s = 2
		case len(q) >= 4:
			for _, w := range strings.Fields(l) {
				if within1(q, w) {
					s = 1
				}
			}
		}
		best = max(best, s)
	}
	return best
}

// within1 reports whether a and b differ by at most one inserted, deleted or
// changed letter.
func within1(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if len(a) == len(b) {
		return i == len(a) || a[i+1:] == b[i+1:]
	}
	return a[i:] == b[i+1:]
}

// readDesktop returns the Name, GenericName and Keywords of the [Desktop
// Entry] group, or nil for an entry marked NoDisplay or Hidden.
func readDesktop(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var labels []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[Desktop Entry]"
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !in || !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Name", "GenericName":
			labels = append(labels, v)
		case "Keywords":
			for _, kw := range strings.Split(v, ";") {
				if kw = strings.TrimSpace(kw); kw != "" {
					labels = append(labels, kw)
				}
			}
		case "NoDisplay", "Hidden":
			if v == "true" {
				return nil
			}
		}
	}
	return labels
}
