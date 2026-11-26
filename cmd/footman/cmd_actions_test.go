//go:build unix

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/api"
)

// Every action lands in the action log and `footman actions` shows it: a
// fast-path command and a setting changed through the API.
func TestActionsCommand(t *testing.T) {
	d := startDaemon(t)
	if code, _, errOut := d.cli(t, "say", "open github"); code != 0 {
		t.Fatalf("say: %s", errOut)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := api.Dial(ctx, d.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Call(ctx, "config.set", api.ConfigSetParams{Key: "dictation.insert", Value: []byte(`"paste"`)}, nil); err != nil {
		t.Fatal(err)
	}

	// The log is written in the background; wait for both entries.
	var out string
	for deadline := time.Now().Add(5 * time.Second); ; {
		code, stdout, errOut := d.cli(t, "actions", "-n", "5")
		if code != 0 {
			t.Fatalf("actions = %d: %s", code, errOut)
		}
		out = stdout
		if strings.Contains(out, "dictation.insert") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{
		"api    fast_path open", "result: Opening github.", "confirmation: auto", "because: open github",
		"api    setting dictation.insert auto → paste", "because: config.set",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("footman actions output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "fast_path") > strings.Index(out, "setting") {
		t.Errorf("entries not oldest first:\n%s", out)
	}

	code, stdout, _ := d.cli(t, "actions", "--json", "-n", "1")
	if code != 0 || !strings.Contains(stdout, `"actions":[{`) || strings.Count(stdout, `"id":`) != 1 {
		t.Errorf("actions --json -n 1 = %d %s", code, stdout)
	}
	if code, _, _ := d.cli(t, "actions", "-n", "0"); code != 2 {
		t.Errorf("actions -n 0 exited %d, want 2", code)
	}
}

func TestPrintActionsEmpty(t *testing.T) {
	var b bytes.Buffer
	printActions(&b, nil)
	if !strings.Contains(b.String(), "empty") {
		t.Errorf("output %q", b.String())
	}
}
