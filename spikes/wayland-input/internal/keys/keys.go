// Package keys maps runes to X keysyms and key names to evdev codes.
package keys

import (
	"fmt"
	"strings"
)

// Keysym returns the X11 keysym for r, following the rule in
// xkbcommon/keysyms: Latin-1 maps 1:1, everything else is 0x01000000+codepoint.
func Keysym(r rune) uint32 {
	switch r {
	case '\n':
		return 0xff0d // Return
	case '\t':
		return 0xff09 // Tab
	case '\b':
		return 0xff08 // BackSpace
	}
	if (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) {
		return uint32(r)
	}
	return 0x01000000 + uint32(r)
}

// Evdev key codes (linux/input-event-codes.h) for the few keys the spikes use.
var Evdev = map[string]uint32{
	"esc": 1, "enter": 28, "tab": 15, "space": 57, "backspace": 14,
	"ctrl": 29, "lctrl": 29, "rctrl": 97, "shift": 42, "lshift": 42, "rshift": 54,
	"alt": 56, "lalt": 56, "ralt": 100, "meta": 125, "super": 125, "lmeta": 125, "rmeta": 126,
	"a": 30, "b": 48, "c": 46, "d": 32, "e": 18, "f": 33, "g": 34, "h": 35, "i": 23,
	"j": 36, "k": 37, "l": 38, "m": 50, "n": 49, "o": 24, "p": 25, "q": 16, "r": 19,
	"s": 31, "t": 20, "u": 22, "v": 47, "w": 17, "x": 45, "y": 21, "z": 44,
	"f1": 59, "f2": 60, "f3": 61, "f4": 62, "f5": 63, "f6": 64, "f7": 65, "f8": 66,
	"f9": 67, "f10": 68, "f11": 87, "f12": 88, "pause": 119, "scrolllock": 70,
	"capslock": 58, "insert": 110, "home": 102, "end": 107,
}

// Combo parses "ctrl+shift+v" into evdev codes (modifiers first).
func Combo(s string) ([]uint32, error) {
	var out []uint32
	for _, p := range strings.Split(strings.ToLower(s), "+") {
		c, ok := Evdev[p]
		if !ok {
			return nil, fmt.Errorf("unknown key %q", p)
		}
		out = append(out, c)
	}
	return out, nil
}
