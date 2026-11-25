//go:build linux

package linux

import (
	"fmt"
	"slices"
	"strings"
)

// hotkey is a parsed key combination such as "Ctrl+Shift+Space".
type hotkey struct {
	combo string // as given, reported back in events
	mods  []string
	key   hotkeyKey
}

// hotkeyKey is a non-modifier key: its xkb keysym name (used in XDG shortcut
// triggers) and its Qt key code (used by KGlobalAccel).
type hotkeyKey struct {
	xkb string
	qt  int32
}

// Modifiers in a fixed order: canonical name, XDG trigger name, Qt bit.
var hotkeyMods = []struct {
	name, xdg string
	qt        int32
	aliases   []string
}{
	{"Ctrl", "CTRL", 0x04000000, []string{"ctrl", "control"}},
	{"Alt", "ALT", 0x08000000, []string{"alt"}},
	{"Shift", "SHIFT", 0x02000000, []string{"shift"}},
	{"Meta", "LOGO", 0x10000000, []string{"meta", "super", "logo", "win", "windows"}},
}

// hotkeyKeys maps lower-case key names to the key.
var hotkeyKeys = func() map[string]hotkeyKey {
	m := map[string]hotkeyKey{
		"space": {"space", 0x20}, "escape": {"Escape", 0x01000000}, "esc": {"Escape", 0x01000000},
		"tab": {"Tab", 0x01000001}, "backspace": {"BackSpace", 0x01000003},
		"return": {"Return", 0x01000004}, "enter": {"Return", 0x01000004},
		"insert": {"Insert", 0x01000006}, "ins": {"Insert", 0x01000006},
		"delete": {"Delete", 0x01000007}, "del": {"Delete", 0x01000007},
		"pause": {"Pause", 0x01000008}, "print": {"Print", 0x01000009},
		"home": {"Home", 0x01000010}, "end": {"End", 0x01000011},
		"left": {"Left", 0x01000012}, "up": {"Up", 0x01000013}, "right": {"Right", 0x01000014}, "down": {"Down", 0x01000015},
		"pageup": {"Prior", 0x01000016}, "pgup": {"Prior", 0x01000016},
		"pagedown": {"Next", 0x01000017}, "pgdown": {"Next", 0x01000017},
		"capslock": {"Caps_Lock", 0x01000024}, "numlock": {"Num_Lock", 0x01000025},
		"scrolllock": {"Scroll_Lock", 0x01000026}, "menu": {"Menu", 0x01000055},
	}
	for i := 1; i <= 35; i++ {
		m[fmt.Sprintf("f%d", i)] = hotkeyKey{fmt.Sprintf("F%d", i), 0x01000030 + int32(i-1)}
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = hotkeyKey{string(c), c - 'a' + 'A'}
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = hotkeyKey{string(c), c}
	}
	return m
}()

// parseHotkey parses "Ctrl+Shift+Space", "F8" or "Super+Space"
// (case-insensitive; Super, Meta, Logo and Win are the same key).
func parseHotkey(combo string) (hotkey, error) {
	parts := strings.Split(strings.TrimSpace(combo), "+")
	h := hotkey{combo: combo}
	seen := map[string]bool{}
	for i, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			return hotkey{}, fmt.Errorf("hotkey %q: empty key name", combo)
		}
		if i < len(parts)-1 {
			found := false
			for _, m := range hotkeyMods {
				if slices.Contains(m.aliases, p) {
					found = true
					seen[m.name] = true
				}
			}
			if !found {
				return hotkey{}, fmt.Errorf("hotkey %q: %q is not a modifier (use Ctrl, Alt, Shift or Super)", combo, p)
			}
			continue
		}
		k, ok := hotkeyKeys[p]
		if !ok {
			return hotkey{}, fmt.Errorf("hotkey %q: unknown key %q", combo, p)
		}
		h.key = k
	}
	for _, m := range hotkeyMods {
		if seen[m.name] {
			h.mods = append(h.mods, m.name)
		}
	}
	return h, nil
}

// trigger is the combination in the XDG shortcuts format the GlobalShortcuts
// portal takes as preferred_trigger, such as "CTRL+SHIFT+space".
func (h hotkey) trigger() string {
	var parts []string
	for _, m := range hotkeyMods {
		for _, x := range h.mods {
			if x == m.name {
				parts = append(parts, m.xdg)
			}
		}
	}
	return strings.Join(append(parts, h.key.xkb), "+")
}

// qtKey is the combination as a Qt key code, for KGlobalAccel.
func (h hotkey) qtKey() int32 {
	v := h.key.qt
	for _, m := range hotkeyMods {
		for _, x := range h.mods {
			if x == m.name {
				v |= m.qt
			}
		}
	}
	return v
}

// id is the shortcut id Footman registers the combination under. It is
// derived from the combination, so the daemon and setup agree on it, and so
// that choosing a new key in setup registers a new shortcut that the desktop
// binds to that key.
func (h hotkey) id() string {
	parts := append([]string(nil), h.mods...)
	parts = append(parts, h.key.xkb)
	return "hotkey-" + strings.ToLower(strings.Join(parts, "-"))
}

// description is the shortcut's name in the desktop's shortcut settings.
func (h hotkey) description() string {
	return "Footman (" + h.combo + ")"
}
