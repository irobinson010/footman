//go:build linux && integration

package linux

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// These tests need a real desktop session (KDE Plasma on Wayland is the
// reference). See README.md in this directory.

func realPlatform(t *testing.T) *Platform {
	t.Helper()
	p, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestRealOpenURL(t *testing.T) {
	p := realPlatform(t)
	if err := p.Launcher().OpenURL(context.Background(), "https://example.com/?footman=integration-test"); err != nil {
		t.Fatal(err)
	}
	t.Log("a browser tab for example.com should have opened")
}

func TestRealNotificationAction(t *testing.T) {
	p := realPlatform(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ch, err := p.Notifier().Notify(ctx, platform.Notification{
		Title:   "Footman integration test",
		Body:    "Click the \"Confirm\" button within 60 seconds.",
		Actions: []platform.Action{{ID: "confirm", Label: "Confirm"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("waiting for you to click the Confirm button...")
	select {
	case id, ok := <-ch:
		if !ok || id != "confirm" {
			t.Fatalf("got %q (open=%v), want a click on confirm", id, ok)
		}
	case <-ctx.Done():
		t.Fatal("no click within 60 seconds")
	}
}

func TestRealSecrets(t *testing.T) {
	s := realPlatform(t).Secrets()
	ctx := context.Background()
	const key = "footman-integration-test"
	t.Cleanup(func() { _ = s.Delete(ctx, key) })
	if err := s.Set(ctx, key, "s3cret ünïcode"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, key)
	if err != nil || got != "s3cret ünïcode" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestRealActiveWindow(t *testing.T) {
	w := realPlatform(t).Windows()
	ctx := context.Background()
	t.Log("the window focused now is the one reported; run this from Konsole")
	a, err := w.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("active window: app=%q title=%q pid=%d id=%s", a.App, a.Title, a.PID, a.ID)
	if a.App == "" {
		t.Error("empty app class")
	}
	if all, err := w.List(ctx); err != nil || len(all) == 0 {
		t.Errorf("List = %v, %v", all, err)
	}
}

func TestRealClipboard(t *testing.T) {
	c := realPlatform(t).Clipboard()
	ctx := context.Background()
	old, _ := c.ReadText(ctx)
	t.Cleanup(func() { _ = c.WriteText(ctx, old) })
	if err := c.WriteText(ctx, "footman clipboard ✓"); err != nil {
		t.Fatal(err)
	}
	if got, err := c.ReadText(ctx); err != nil || got != "footman clipboard ✓" {
		t.Fatalf("ReadText = %q, %v", got, err)
	}
}
