//go:build linux

package linux

import (
	"slices"
	"testing"
)

func TestParseCombo(t *testing.T) {
	for combo, want := range map[string][]uint32{
		"Enter":        {keysymReturn},
		"Ctrl+End":     {keysymControlL, keysymEnd},
		"ctrl+shift+Z": {keysymControlL, keysymShiftL, 'z'},
		"Ctrl+S":       {keysymControlL, 's'},
		"Escape":       {keysymEscape},
	} {
		got, err := parseCombo(combo)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("parseCombo(%q) = %#x, %v; want %#x", combo, got, err, want)
		}
	}
	for _, bad := range []string{"", "Ctrl+", "Hyper+A", "Ctrl+Foo", "Ctrl+é"} {
		if got, err := parseCombo(bad); err == nil {
			t.Errorf("parseCombo(%q) = %#x, want an error", bad, got)
		}
	}
}
