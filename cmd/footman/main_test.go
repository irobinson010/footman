package main

import (
	"bytes"
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
		{[]string{"help"}, 0, "Usage:", ""},
		{nil, 2, "", "Usage:"},
		{[]string{"nope"}, 2, "", "unknown command"},
	}
	for _, tt := range tests {
		var out, errb bytes.Buffer
		if got := run(tt.args, &out, &errb); got != tt.code {
			t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.code)
		}
		if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
			t.Errorf("run(%v) out=%q err=%q", tt.args, out.String(), errb.String())
		}
	}
}
