//go:build linux

package linux

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/registry"
)

func TestRegistered(t *testing.T) {
	if !platform.Platforms.Has(Name) {
		t.Fatal("linux platform not registered")
	}
	p, err := platform.Platforms.New(Name, registry.Map{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.Input().Type(ctx, "x"); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Input.Type = %v, want ErrUnsupported", err)
	}
	if _, err := p.Audio().Inputs(ctx); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Audio.Inputs = %v, want ErrUnsupported", err)
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "relative/ignored")
	t.Setenv("XDG_CACHE_HOME", "/c")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	p := &Paths{}
	for got, want := range map[string]string{
		p.Config():  "/home/u/.config/footman",
		p.Data():    "/home/u/.local/share/footman",
		p.Cache():   "/c/footman",
		p.Runtime(): "/run/user/1000",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if r := p.Runtime(); !filepath.IsAbs(r) || !strings.Contains(r, "footman-") {
		t.Errorf("fallback runtime = %q", r)
	}
}

func recorder(res runResult, err error) (runFunc, *[]runSpec) {
	var calls []runSpec
	return func(_ context.Context, s runSpec) (runResult, error) {
		calls = append(calls, s)
		return res, err
	}, &calls
}

func TestLauncher(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	writeFile(t, filepath.Join(data, "applications", "org.kde.konsole.desktop"), "[Desktop Entry]\nName=Konsole\n")
	writeFile(t, filepath.Join(data, "applications", "firefox.desktop"), "[Desktop Entry]\nName=Web Browser\n")
	writeFile(t, filepath.Join(data, "applications", "hidden.desktop"), "[Desktop Entry]\nName=Ghost\nNoDisplay=true\n")
	run, calls := recorder(runResult{}, nil)
	l := &Launcher{run: run, dataDirs: func() []string { return []string{data} }}

	if err := l.OpenURL(ctx, "https://example.com/a b"); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[0]; got.Name != "xdg-open" || got.Args[len(got.Args)-1] != "https://example.com/a b" {
		t.Errorf("OpenURL ran %+v", got)
	}
	for _, bad := range []string{"", "example.com", "file:///etc/passwd", "javascript:alert(1)", "-x"} {
		if err := l.OpenURL(ctx, bad); err == nil {
			t.Errorf("OpenURL(%q) succeeded", bad)
		}
	}

	for _, app := range []string{"org.kde.konsole", "org.kde.konsole.desktop", "konsole", "Konsole", "Web Browser"} {
		*calls = nil
		if err := l.OpenApp(ctx, app); err != nil {
			t.Fatalf("OpenApp(%q): %v", app, err)
		}
		got := (*calls)[0]
		if got.Name != "gio" || got.Args[0] != "launch" || !strings.HasSuffix(got.Args[1], ".desktop") {
			t.Errorf("OpenApp(%q) ran %+v", app, got)
		}
	}
	for _, app := range []string{"nope", "Ghost", "-evil", ""} {
		if err := l.OpenApp(ctx, app); err == nil {
			t.Errorf("OpenApp(%q) succeeded", app)
		} else if app == "nope" && !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("OpenApp(nope) = %v, want ErrNotFound", err)
		}
	}

	file := filepath.Join(data, "f.txt")
	writeFile(t, file, "x")
	if err := l.OpenFile(ctx, file); err != nil {
		t.Fatal(err)
	}
	if err := l.OpenFile(ctx, file+".missing"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("OpenFile(missing) = %v", err)
	}

	missing, _ := recorder(runResult{}, errNoTool)
	l.run = missing
	if err := l.OpenURL(ctx, "https://x.org"); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("no xdg-open: %v", err)
	}
}

func TestClipboard(t *testing.T) {
	ctx := context.Background()
	run, calls := recorder(runResult{Stdout: "héllo\n"}, nil)
	c := &Clipboard{run: run}
	got, err := c.ReadText(ctx)
	if err != nil || got != "héllo\n" {
		t.Errorf("ReadText = %q, %v", got, err)
	}
	if err := c.WriteText(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	if s := (*calls)[1]; s.Name != "wl-copy" || s.Stdin != "hi" {
		t.Errorf("WriteText ran %+v", s)
	}
	if err := c.WriteText(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s := (*calls)[2]; s.Args[0] != "--clear" {
		t.Errorf("clearing ran %+v", s)
	}

	empty, _ := recorder(runResult{Stderr: "Nothing is copied"}, errors.New("exit status 1"))
	c.run = empty
	if got, err := c.ReadText(ctx); err != nil || got != "" {
		t.Errorf("empty clipboard = %q, %v", got, err)
	}
	none, _ := recorder(runResult{}, errNoTool)
	c.run = none
	if _, err := c.ReadText(ctx); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("no wl-paste: %v", err)
	}
}
