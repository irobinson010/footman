//go:build unix

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/permissions"
	"github.com/irobinson010/footman/internal/platform"
	platformfake "github.com/irobinson010/footman/internal/platform/fake"
)

// eventually waits for cond, failing with what after a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (d *testDaemon) setting(t *testing.T, key string) any {
	t.Helper()
	c, err := config.Load(d.config)
	if err != nil {
		t.Fatal(err)
	}
	return c.Get(key)
}

// confirmNote returns the index of the latest notification with buttons.
func confirmNote(n *platformfake.Notifier) (int, platform.Notification, bool) {
	shown := n.Shown()
	for i := len(shown) - 1; i >= 0; i-- {
		if len(shown[i].Actions) > 0 {
			return i, shown[i], true
		}
	}
	return 0, platform.Notification{}, false
}

// The issue's done-when: "turn off the action log" by voice changes nothing
// until Allow is clicked in the notification or with `footman confirm
// allow`, and "set destructive actions to deny" takes effect at once.
func TestConfirmByCLIAndNotification(t *testing.T) {
	d := startDaemon(t)
	notes := d.platform.Notifier().(*platformfake.Notifier)
	say := func(text string) {
		t.Helper()
		if code, _, errOut := d.cli(t, "say", text); code != 0 {
			t.Fatalf("say %q: %s", text, errOut)
		}
	}

	// Tightening by voice applies at once.
	say("set destructive actions to deny")
	if got := d.setting(t, "permissions.destructive"); got != "deny" {
		t.Errorf("tightening by voice: permissions.destructive = %v", got)
	}

	// Loosening waits for the notification's Allow button.
	say("set destructive actions to confirm")
	var i int
	eventually(t, "a confirmation notification", func() bool {
		var note platform.Notification
		var ok bool
		i, note, ok = confirmNote(notes)
		return ok && note.Title == "Change permissions.destructive?"
	})
	if got := d.setting(t, "permissions.destructive"); got != "deny" {
		t.Fatalf("loosened before the click: %v", got)
	}
	notes.Click(i, permissions.ActionAllow)
	eventually(t, "permissions.destructive = confirm", func() bool { return d.setting(t, "permissions.destructive") == "confirm" })
	// It is logged as a click.
	eventually(t, "the clicked change in the action log", func() bool {
		_, out, _ := d.cli(t, "actions", "-n", "20")
		return strings.Contains(out, "setting permissions.destructive deny → confirm") && strings.Contains(out, "confirmation: click")
	})

	// "Turn off the action log" waits for footman confirm allow.
	say("turn off the action log")
	if got := d.setting(t, "permissions.action_log"); got != true {
		t.Fatalf("action log changed by voice alone: %v", got)
	}
	code, out, _ := d.cli(t, "confirm")
	if code != 0 || !strings.Contains(out, "2  Change permissions.action_log?") || !strings.Contains(out, "permissions.action_log: true → false") {
		t.Fatalf("footman confirm = %d:\n%s", code, out)
	}
	if code, out, _ := d.cli(t, "confirm", "--json"); code != 0 || !strings.Contains(out, `"click_only":true`) {
		t.Errorf("footman confirm --json = %d %s", code, out)
	}
	if code, out, errOut := d.cli(t, "confirm", "allow", "2"); code != 0 || out != "Allowed 2.\n" {
		t.Fatalf("footman confirm allow 2 = %d %q %s", code, out, errOut)
	}
	eventually(t, "the action log to be off", func() bool { return d.setting(t, "permissions.action_log") == false })
	if code, _, errOut := d.cli(t, "confirm", "allow", "2"); code == 0 || !strings.Contains(errOut, "no pending confirmation") {
		t.Errorf("second allow = %d %s", code, errOut)
	}
	if code, out, _ := d.cli(t, "confirm"); code != 0 || !strings.Contains(out, "Nothing is waiting") {
		t.Errorf("footman confirm after = %d %s", code, out)
	}

	// Turning it back on is tightening; turning it off again and denying
	// changes nothing.
	say("turn on the action log")
	if got := d.setting(t, "permissions.action_log"); got != true {
		t.Errorf("tightening the action log by voice: %v", got)
	}
	say("turn off the action log")
	if code, out, errOut := d.cli(t, "confirm", "deny", "3"); code != 0 || out != "Denied 3.\n" {
		t.Fatalf("footman confirm deny 3 = %d %q %s", code, out, errOut)
	}
	time.Sleep(50 * time.Millisecond)
	if got := d.setting(t, "permissions.action_log"); got != true {
		t.Errorf("denied change applied: %v", got)
	}

	if code, _, _ := d.cli(t, "confirm", "maybe", "1"); code != 2 {
		t.Errorf("bad usage exited %d, want 2", code)
	}
}
