//go:build linux

package linux

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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
	// Never call Input here: on a desktop it would reach the real session.
	if _, ok := p.Input().(platform.InputSetup); !ok {
		t.Error("Input does not implement platform.InputSetup")
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

func TestLauncherFuzzyApps(t *testing.T) {
	data := t.TempDir()
	writeFile(t, filepath.Join(data, "applications", "org.mozilla.firefox.desktop"),
		"[Desktop Entry]\nName=Firefox Web Browser\nGenericName=Web Browser\nKeywords=internet;www;\n")
	writeFile(t, filepath.Join(data, "applications", "code.desktop"), "[Desktop Entry]\nName=Visual Studio Code\nGenericName=Text Editor\n")
	l := &Launcher{dataDirs: func() []string { return []string{data} }}
	for app, want := range map[string]string{
		"firefox": "org.mozilla.firefox.desktop", "browser": "org.mozilla.firefox.desktop",
		"internet": "org.mozilla.firefox.desktop", "firefax": "org.mozilla.firefox.desktop",
		"studio": "code.desktop", "text editor": "code.desktop",
	} {
		if got := filepath.Base(l.findDesktopFile(app)); got != want {
			t.Errorf("findDesktopFile(%q) = %q, want %q", app, got, want)
		}
	}
	if got := l.findDesktopFile("zzzzz"); got != "" {
		t.Errorf("findDesktopFile(zzzzz) = %q", got)
	}
}

func TestVolume(t *testing.T) {
	run, calls := recorder(runResult{}, nil)
	p := newPlatform(nil, run)
	ctx := context.Background()
	_ = p.ChangeVolume(ctx, -10)
	_ = p.ChangeVolume(ctx, 10)
	_ = p.SetVolume(ctx, 250)
	_ = p.SetMute(ctx, true)
	_ = p.SetMute(ctx, false)
	var got []string
	for _, c := range *calls {
		got = append(got, c.Name+" "+strings.Join(c.Args, " "))
	}
	want := []string{
		"pactl set-sink-volume @DEFAULT_SINK@ -10%", "pactl set-sink-volume @DEFAULT_SINK@ +10%",
		"pactl set-sink-volume @DEFAULT_SINK@ 100%",
		"pactl set-sink-mute @DEFAULT_SINK@ 1", "pactl set-sink-mute @DEFAULT_SINK@ 0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("pactl calls\n got %q\nwant %q", got, want)
	}
	missing, _ := recorder(runResult{}, errNoTool)
	if err := newPlatform(nil, missing).SetMute(ctx, true); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("no pactl: %v", err)
	}
}
