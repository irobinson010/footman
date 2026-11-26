//go:build linux

//go:generate go run input_keysyms_gen.go

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// keysymChar is a keysym and the character it types.
type keysymChar struct {
	sym uint32
	r   rune
}

// Keysyms with no character of their own.
const (
	keysymReturn    = 0xff0d
	keysymTab       = 0xff09
	keysymBackSpace = 0xff08
	keysymShiftL    = 0xffe1
	keysymControlL  = 0xffe3
	keysymInsert    = 0xff63
)

// unicodeKeysym returns the keysym for r that needs no keymap: Latin-1 maps
// one to one, everything else is 0x01000000 + code point.
func unicodeKeysym(r rune) uint32 {
	switch r {
	case '\n':
		return keysymReturn
	case '\t':
		return keysymTab
	}
	if (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) {
		return uint32(r)
	}
	return 0x01000000 + uint32(r)
}

// keysymFromName resolves a keysym as written in an xkb keymap: a name
// ("eacute"), "U20AC", or a number ("0x10020ac").
func keysymFromName(s string) (keysymChar, bool) {
	if k, ok := keysymNames[s]; ok {
		return k, true
	}
	var v uint64
	var err error
	switch {
	case len(s) > 1 && s[0] == 'U' && isHex(s[1:]):
		v, err = strconv.ParseUint(s[1:], 16, 32)
		if err == nil && v >= 0x20 {
			return keysymChar{unicodeKeysym(rune(v)), rune(v)}, true
		}
		return keysymChar{}, false
	case strings.HasPrefix(s, "0x"):
		v, err = strconv.ParseUint(s[2:], 16, 32)
	default:
		return keysymChar{}, false
	}
	if err != nil {
		return keysymChar{}, false
	}
	sym := uint32(v)
	switch {
	case sym >= 0x01000100 && sym <= 0x0110ffff:
		return keysymChar{sym, rune(sym - 0x01000000)}, true
	case (sym >= 0x20 && sym <= 0x7e) || (sym >= 0xa0 && sym <= 0xff):
		return keysymChar{sym, rune(sym)}, true
	}
	k, ok := keysymsByValue()[sym]
	return k, ok
}

// keysymsByValue indexes keysymNames by keysym, since compositors usually
// write keymaps with numbers rather than names.
var keysymsByValue = sync.OnceValue(func() map[uint32]keysymChar {
	m := make(map[uint32]keysymChar, len(keysymNames))
	for _, k := range keysymNames {
		m[k.sym] = k
	}
	return m
})

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !unicode.Is(unicode.ASCII_Hex_Digit, c) {
			return false
		}
	}
	return true
}

// keymap is the typeable characters of each layout group of an xkb keymap.
type keymap struct {
	groups []map[rune]uint32 // per group: character -> keysym that types it
}

// typeable reports whether every character of text is on the group's
// layout. Newlines and tabs count as typeable (Return and Tab).
func (k *keymap) typeable(text string, group int) bool {
	if group < 0 || group >= len(k.groups) {
		group = 0
	}
	if len(k.groups) == 0 {
		return false
	}
	g := k.groups[group]
	for _, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}
		if _, ok := g[r]; !ok {
			return false
		}
	}
	return true
}

// keysym returns the keysym that types r on the group's layout, or the
// layout-free Unicode keysym when the layout lacks r.
func (k *keymap) keysym(r rune, group int) uint32 {
	if r == '\n' || r == '\t' {
		return unicodeKeysym(r)
	}
	if k != nil && group >= 0 && group < len(k.groups) {
		if s, ok := k.groups[group][r]; ok {
			return s
		}
	}
	return unicodeKeysym(r)
}

// parseKeymap reads the xkb_symbols section of a keymap in the text format
// compositors send (XKB_KEYMAP_FORMAT_TEXT_V1) and records, per group, every
// character a key can type at any shift level.
func parseKeymap(text string) (*keymap, error) {
	i := strings.Index(text, "xkb_symbols")
	if i < 0 {
		return nil, errors.New("keymap has no xkb_symbols section")
	}
	toks := tokenizeXKB(text[i:])
	// Skip to the section's opening brace.
	p := 0
	for p < len(toks) && toks[p] != "{" {
		p++
	}
	if p == len(toks) {
		return nil, errors.New("keymap: malformed xkb_symbols")
	}
	p++
	km := &keymap{}
	add := func(group int, syms []string) {
		for len(km.groups) <= group {
			km.groups = append(km.groups, map[rune]uint32{})
		}
		for _, s := range syms {
			if k, ok := keysymFromName(s); ok {
				if _, seen := km.groups[group][k.r]; !seen {
					km.groups[group][k.r] = k.sym
				}
			}
		}
	}
	depth := 1
	for p < len(toks) && depth > 0 {
		t := toks[p]
		switch {
		case t == "{":
			depth++
			p++
		case t == "}":
			depth--
			p++
		case t == "key" && depth == 1:
			p = parseKey(toks, p+1, add)
		default:
			p++
		}
	}
	if len(km.groups) == 0 {
		return nil, errors.New("keymap has no symbols")
	}
	return km, nil
}

// parseKey parses `<NAME> { ... };` starting at p and returns the position
// after it.
func parseKey(toks []string, p int, add func(int, []string)) int {
	for p < len(toks) && toks[p] != "{" {
		if toks[p] == ";" {
			return p + 1
		}
		p++
	}
	p++ // past "{"
	implicit := 0
	for p < len(toks) && toks[p] != "}" {
		t := toks[p]
		switch {
		case t == "[":
			syms, np := parseList(toks, p)
			add(implicit, syms)
			implicit++
			p = np
		case t == "symbols" && p+1 < len(toks) && toks[p+1] == "[":
			// symbols[Group2]= [ ... ]  or  symbols[2]= [ ... ]
			idx := 0
			if p+2 < len(toks) {
				idx = groupIndex(toks[p+2])
			}
			p += 2
			for p < len(toks) && toks[p] != "=" {
				p++
			}
			p++
			if p < len(toks) && toks[p] == "[" && idx >= 0 {
				syms, np := parseList(toks, p)
				add(idx, syms)
				p = np
			}
		case isIdent(t):
			// Some other field (type, actions[...], vmods, repeat, ...):
			// skip its value.
			p++
			if p < len(toks) && toks[p] == "[" {
				_, p = parseList(toks, p)
			}
			if p < len(toks) && toks[p] == "=" {
				p++
				if p < len(toks) && toks[p] == "[" {
					_, p = parseList(toks, p)
				} else if p < len(toks) {
					p++
				}
			}
		default:
			p++
		}
	}
	p++ // past "}"
	if p < len(toks) && toks[p] == ";" {
		p++
	}
	return p
}

// parseList parses a bracketed list starting at toks[p] == "[" and returns
// its plain entries (entries inside { } and ( ) are skipped) and the
// position after the closing bracket.
func parseList(toks []string, p int) ([]string, int) {
	var out []string
	depth := 0
	for ; p < len(toks); p++ {
		t := toks[p]
		switch t {
		case "[", "{", "(":
			depth++
		case "]", "}", ")":
			depth--
			if depth == 0 {
				return out, p + 1
			}
		default:
			if depth == 1 && t != "," {
				out = append(out, t)
			}
		}
	}
	return out, p
}

func groupIndex(s string) int {
	s = strings.TrimPrefix(strings.ToLower(s), "group")
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 32 {
		return -1
	}
	return n - 1
}

func isIdent(t string) bool {
	if t == "" {
		return false
	}
	c := t[0]
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tokenizeXKB splits xkb keymap text into identifiers, numbers, strings and
// single punctuation characters, dropping comments.
func tokenizeXKB(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/', c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return out
			}
			i += end + 4
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, s[i:min(j+1, len(s))])
			i = j + 1
		case c == '<':
			j := strings.IndexByte(s[i:], '>')
			if j < 0 {
				return out
			}
			out = append(out, s[i:i+j+1])
			i += j + 1
		case c == '_' || c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i
			for j < len(s) {
				d := s[j]
				if d == '_' || d == '+' || d == '-' || d == '.' || (d >= '0' && d <= '9') || (d >= 'a' && d <= 'z') || (d >= 'A' && d <= 'Z') {
					j++
					continue
				}
				break
			}
			out = append(out, s[i:j])
			i = j
		default:
			out = append(out, string(c))
			i++
		}
	}
	return out
}

// keyboardWatch keeps the compositor's current keymap, which it sends to
// every wl_keyboard and again whenever the layouts change.
type keyboardWatch struct {
	mu    sync.Mutex
	km    *keymap
	err   error
	ready chan struct{}
	once  sync.Once
}

// watchKeyboard binds a keyboard on the seat to receive keymaps.
func watchKeyboard(w *wlConn) (*keyboardWatch, error) {
	seat, ok := w.global("wl_seat")
	if !ok {
		return nil, errors.New("the compositor has no seat")
	}
	seatID, _, err := w.bind(seat, 1)
	if err != nil {
		return nil, err
	}
	kw := &keyboardWatch{ready: make(chan struct{})}
	kb := w.newID()
	w.handle(kb, func(op uint16, body []byte, fd func() int) {
		if op != 0 { // wl_keyboard.keymap
			return
		}
		r := wlReader{b: body}
		format := r.uint()
		f := fd()
		size := r.uint()
		if f < 0 {
			return
		}
		km, err := readKeymap(f, format, size)
		kw.mu.Lock()
		if err == nil {
			kw.km, kw.err = km, nil
		} else if kw.km == nil {
			kw.err = err
		}
		kw.mu.Unlock()
		kw.once.Do(func() { close(kw.ready) })
	})
	if err := w.send(seatID, wlSeatGetKeyboard, nil, wlUint(kb)); err != nil {
		return nil, err
	}
	return kw, nil
}

func readKeymap(fd int, format, size uint32) (*keymap, error) {
	f := os.NewFile(uintptr(fd), "keymap")
	defer func() { _ = f.Close() }()
	if format != 1 { // WL_KEYBOARD_KEYMAP_FORMAT_XKB_V1
		return nil, fmt.Errorf("unsupported keymap format %d", format)
	}
	if size == 0 || size > 16<<20 {
		return nil, fmt.Errorf("bad keymap size %d", size)
	}
	buf := make([]byte, size)
	n, err := f.ReadAt(buf, 0)
	if n == 0 && err != nil {
		// Some compositors send a pipe-like fd that cannot be read at an
		// offset; map it instead.
		data, merr := syscall.Mmap(fd, 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
		if merr != nil {
			return nil, fmt.Errorf("reading keymap: %w", err)
		}
		copy(buf, data)
		_ = syscall.Munmap(data)
		n = len(buf)
	}
	return parseKeymap(strings.TrimRight(string(buf[:n]), "\x00"))
}

// current returns the latest keymap, waiting up to wait for the first one.
func (kw *keyboardWatch) current(ctx context.Context, wait time.Duration) (*keymap, error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-kw.ready:
	case <-t.C:
		return nil, errors.New("the compositor sent no keymap")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	kw.mu.Lock()
	defer kw.mu.Unlock()
	return kw.km, kw.err
}

// asciiTypeable is the fallback when the keymap is unknown: printable ASCII
// is on almost every Latin layout.
func asciiTypeable(text string) bool {
	for _, r := range text {
		if (r < ' ' || r > '~') && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
