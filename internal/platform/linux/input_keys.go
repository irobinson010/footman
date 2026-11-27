//go:build linux

package linux

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Keysyms for the named keys InputKeys.Key accepts (X11 keysymdef.h).
const (
	keysymAltL   = 0xffe9
	keysymHome   = 0xff50
	keysymLeft   = 0xff51
	keysymUp     = 0xff52
	keysymRight  = 0xff53
	keysymDown   = 0xff54
	keysymEnd    = 0xff57
	keysymEscape = 0xff1b
	keysymDelete = 0xffff
)

var namedKeys = map[string]uint32{
	"enter": keysymReturn, "return": keysymReturn, "tab": keysymTab, "escape": keysymEscape, "esc": keysymEscape,
	"home": keysymHome, "end": keysymEnd, "up": keysymUp, "down": keysymDown, "left": keysymLeft, "right": keysymRight,
	"backspace": keysymBackSpace, "delete": keysymDelete,
}

var modifierKeys = map[string]uint32{
	"ctrl": keysymControlL, "control": keysymControlL, "shift": keysymShiftL, "alt": keysymAltL,
}

// parseCombo turns "Ctrl+Shift+Z" into the keysyms to press in order:
// modifiers first, then the key.
func parseCombo(combo string) ([]uint32, error) {
	parts := strings.Split(combo, "+")
	var syms []uint32
	for i, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if i < len(parts)-1 {
			m, ok := modifierKeys[p]
			if !ok {
				return nil, fmt.Errorf("linux input: unknown modifier %q in %q", p, combo)
			}
			syms = append(syms, m)
			continue
		}
		if k, ok := namedKeys[p]; ok {
			return append(syms, k), nil
		}
		if r := []rune(p); len(r) == 1 && r[0] < 0x80 && (unicode.IsLower(r[0]) || unicode.IsDigit(r[0])) {
			return append(syms, uint32(r[0])), nil
		}
		return nil, fmt.Errorf("linux input: unknown key %q in %q", p, combo)
	}
	return nil, fmt.Errorf("linux input: empty key combination")
}

// Key implements platform.InputKeys through the RemoteDesktop portal session,
// the same one typing uses.
func (in *Input) Key(ctx context.Context, combo string) error {
	syms, err := parseCombo(combo)
	if err != nil {
		return err
	}
	in.insertMu.Lock()
	defer in.insertMu.Unlock()
	p, s, err := in.remote.ensure(ctx)
	if err != nil {
		return err
	}
	return in.remote.combo(ctx, p, s, syms)
}
