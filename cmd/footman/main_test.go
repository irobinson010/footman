package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		args    []string
		code    int
		wantOut string
		wantErr string
	}{
		{[]string{"version"}, 0, "footman dev\n", ""},
		{[]string{"--version"}, 0, "footman dev\n", ""},
		{[]string{"help"}, 0, "Usage:", ""},
		{nil, 2, "", "Usage:"},
		{[]string{"nope"}, 2, "", "unknown command"},
		{[]string{"say"}, 2, "", "Usage: footman say"},
		{[]string{"stop", "now"}, 2, "", "Usage: footman stop"},
		{[]string{"daemon", "extra"}, 2, "", "Usage: footman daemon"},
		{[]string{"daemon", "-h"}, 0, "", "-dev-fakes"},
	}
	for _, tt := range tests {
		var out, errb bytes.Buffer
		if got := run(context.Background(), tt.args, &out, &errb); got != tt.code {
			t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.code)
		}
		if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
			t.Errorf("run(%v) out=%q err=%q", tt.args, out.String(), errb.String())
		}
	}
}

func TestHelpListsCommands(t *testing.T) {
	var out bytes.Buffer
	run(context.Background(), []string{"help"}, &out, io.Discard)
	for _, name := range []string{"daemon", "events", "help", "say", "setup", "status", "stop", "uninstall", "version"} {
		if !strings.Contains(out.String(), "\n  "+name+" ") {
			t.Errorf("help does not list %s:\n%s", name, out.String())
		}
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering status twice did not panic")
		}
	}()
	register(command{name: "status", run: cmdStatus})
}
