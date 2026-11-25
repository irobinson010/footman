package fake_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
)

func newPlatform(t *testing.T) platform.Platform {
	t.Helper()
	p, err := platform.Platforms.New(fake.Name, registry.Map{"root": "/fake"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInput(t *testing.T) {
	p := newPlatform(t)
	in := p.Input().(*fake.Input)
	ctx, cancel := context.WithCancel(context.Background())

	if err := p.Input().Type(ctx, "Hello there."); err != nil {
		t.Fatal(err)
	}
	_ = p.Input().Paste(ctx, "pasted")
	if !reflect.DeepEqual(in.Typed(), []string{"Hello there."}) || !reflect.DeepEqual(in.Pasted(), []string{"pasted"}) {
		t.Fatalf("typed %q, pasted %q", in.Typed(), in.Pasted())
	}

	ch, err := p.Input().Hotkeys(ctx, []string{"Super+Space"})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan platform.HotkeyEvent, 2)
	go func() {
		for e := range ch {
			got <- e
		}
		close(got)
	}()
	if n := in.Press("Super+Space", true); n != 1 {
		t.Fatalf("Press delivered to %d", n)
	}
	if n := in.Press("Ctrl+C", true); n != 0 {
		t.Fatalf("unregistered combo delivered to %d", n)
	}
	if e := <-got; e.Hotkey != "Super+Space" || !e.Pressed {
		t.Fatalf("event = %+v", e)
	}
	cancel()
	if _, ok := <-got; ok {
		t.Fatal("hotkey channel not closed after cancel")
	}
}

func TestLauncherWindowsClipboard(t *testing.T) {
	p := newPlatform(t)
	ctx := context.Background()

	_ = p.Launcher().OpenURL(ctx, "https://youtube.com")
	_ = p.Launcher().OpenApp(ctx, "org.kde.konsole")
	l := p.Launcher().(*fake.Launcher)
	if !reflect.DeepEqual(l.URLs(), []string{"https://youtube.com"}) || len(l.Opened()) != 2 {
		t.Fatalf("opened %+v", l.Opened())
	}

	w := p.Windows().(*fake.Windows)
	w.Set([]platform.Window{{ID: "1", Title: "Editor"}, {ID: "2", Title: "Terminal"}}, "1")
	if err := p.Windows().Focus(ctx, "2"); err != nil {
		t.Fatal(err)
	}
	if a, _ := p.Windows().Active(ctx); a.Title != "Terminal" {
		t.Fatalf("active = %+v", a)
	}
	if err := p.Windows().Focus(ctx, "9"); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("Focus(missing) = %v", err)
	}

	_ = p.Clipboard().WriteText(ctx, "copied")
	if s, _ := p.Clipboard().ReadText(ctx); s != "copied" {
		t.Fatalf("clipboard = %q", s)
	}
}

func TestNotifier(t *testing.T) {
	p := newPlatform(t)
	ctx := context.Background()
	n := p.Notifier().(*fake.Notifier)

	ch, err := p.Notifier().Notify(ctx, platform.Notification{
		Title: "Delete 3 files?", Actions: []platform.Action{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Click(0, "yes") || n.Click(5, "yes") {
		t.Fatal("Click index handling wrong")
	}
	if a := <-ch; a != "yes" {
		t.Fatalf("action = %q", a)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after click")
	}

	cctx, cancel := context.WithCancel(ctx)
	ch, _ = p.Notifier().Notify(cctx, platform.Notification{Title: "x"})
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after cancel")
	}
	if len(n.Shown()) != 2 {
		t.Fatalf("shown %d", len(n.Shown()))
	}

	_ = p.Notifier().SetTray(ctx, platform.Recording)
	if !reflect.DeepEqual(n.Tray(), []platform.TrayStatus{platform.Recording}) {
		t.Fatalf("tray = %v", n.Tray())
	}
}

func TestAudio(t *testing.T) {
	p := newPlatform(t)
	ctx := context.Background()
	a := p.Audio().(*fake.Audio)

	clip := audio.Clip{Format: audio.Speech, Samples: make([]float32, 1600)}
	a.QueueCapture(clip.Frames(160)...)
	ch, err := p.Audio().Capture(ctx, "", audio.Speech)
	if err != nil {
		t.Fatal(err)
	}
	var d time.Duration
	for f := range ch {
		d += f.Duration()
	}
	if d != 100*time.Millisecond {
		t.Fatalf("captured %v", d)
	}
	if _, err := p.Audio().Capture(ctx, "nope", audio.Speech); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("Capture(unknown) = %v", err)
	}

	if err := p.Audio().Play(ctx, "", audio.NewClipStream(clip, 320)); err != nil {
		t.Fatal(err)
	}
	if pl := a.Played(); len(pl) != 1 || len(pl[0].Samples) != 1600 {
		t.Fatalf("played %d clips", len(pl))
	}
}

func TestServiceSecretsPaths(t *testing.T) {
	p := newPlatform(t)
	ctx := context.Background()

	_ = p.Service().Install(ctx)
	if ok, _ := p.Service().Installed(ctx); !ok {
		t.Fatal("not installed")
	}
	_ = p.Service().Uninstall(ctx)
	if ok, _ := p.Service().Installed(ctx); ok {
		t.Fatal("still installed")
	}

	if _, err := p.Secrets().Get(ctx, "claude"); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("Get(missing) = %v", err)
	}
	_ = p.Secrets().Set(ctx, "claude", "sk-test")
	if v, _ := p.Secrets().Get(ctx, "claude"); v != "sk-test" {
		t.Fatalf("secret = %q", v)
	}
	_ = p.Secrets().Delete(ctx, "claude")
	if _, err := p.Secrets().Get(ctx, "claude"); !errors.Is(err, platform.ErrNotFound) {
		t.Fatal("secret survived Delete")
	}

	if p.Paths().Config() != "/fake/config" || p.Paths().Runtime() != "/fake/runtime" {
		t.Fatalf("paths = %s, %s", p.Paths().Config(), p.Paths().Runtime())
	}
}
