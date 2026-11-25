//go:build linux

package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// A keymap in the text format compositors send: KWin writes keysyms as
// numbers and symbols[1]; xkbcommon's older output uses names and bare
// lists. Group 1 is US, group 2 German-ish.
const testKeymap = `xkb_keymap {
xkb_keycodes "evdev" { minimum = 8; maximum = 708; <AE01> = 10; };
xkb_types "complete" { type "ONE_LEVEL" { modifiers= none; level_name[1]= "Any"; }; };
xkb_compatibility "complete" {
	interpret Shift_L+AnyOf(all) { action= SetMods(modifiers=Shift,clearLocks); };
};
xkb_symbols "pc_us_de(evdev)" {
	name[1]="English (US)";
	name[2]="German";
	// a comment with [ brackets ] and { braces }
	key <ESC> {	[ 0xff1b ] };
	key <AE01> {	[ 0x31, 0x21 ], [ 0x31, 0x21 ] };
	key <AE04> {	[ 0x34, 0x24 ], [ 0x34, 0x24, 0x20ac ] };
	key <AE11> {	[ 0x2d, 0x5f ], [ 0xdf, 0x3f ] };
	key <AD01> {
		type= "ALPHABETIC",
		symbols[1]= [ 0x71, 0x51 ],
		symbols[2]= [ 0x71, 0x51, 0x40 ]
	};
	key <AC01> { symbols[Group1]= [ a, A ], symbols[Group2]= [ a, A, ae, AE ] };
	key <AC10> { [ semicolon, colon ], [ odiaeresis, Odiaeresis ] };
	key <AB01> { [ z, Z ], [ y, Y ] };
	key <AB05> { [ U0062, B ] };
	key <AB06> { [ 0x6e, 0x4e, 0x1002014 ] };
	key <SPCE> {	[ 0x20 ] };
	key <RALT> {
		type= "ONE_LEVEL",
		symbols[1]= [ 0xfe03 ],
		actions[1]= [ SetMods(modifiers=Mod5,data[0]=0x1) ]
	};
	key <I999> { [ { c, d }, 0x1008fe01 ] };
	modifier_map Shift { <LFSH> };
};
};
`

func TestParseKeymap(t *testing.T) {
	km, err := parseKeymap(testKeymap)
	if err != nil {
		t.Fatal(err)
	}
	if len(km.groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(km.groups))
	}
	for _, c := range []struct {
		text  string
		group int
		want  bool
	}{
		{"qa1-; zQAbBn!", 0, true},
		{"qa\nz\t1", 0, true},
		{"ß", 0, false},
		{"ß?€öæ@y", 1, true},
		{"z", 1, false},
		{"—", 0, true}, // 0x1002014 at level 3 of <AB06>
		{"c", 0, false},
		{"d", 0, false}, // entries in { } are not single keys
		{"é", 0, false},
		{"q", 7, true}, // an unknown group falls back to the first
	} {
		if got := km.typeable(c.text, c.group); got != c.want {
			t.Errorf("typeable(%q, group %d) = %v, want %v", c.text, c.group, got, c.want)
		}
	}
	for _, c := range []struct {
		r     rune
		group int
		want  uint32
	}{
		{'€', 1, 0x20ac},    // the layout's own keysym, so no keymap swap
		{'€', 0, 0x10020ac}, // not on the layout: the Unicode keysym
		{'ö', 1, 0xf6},      // Latin-1
		{'—', 0, 0x1002014}, // as the keymap writes it
		{'\n', 0, 0xff0d},   // Return
		{'\t', 0, 0xff09},   // Tab
		{'👋', 0, 0x101f44b}, // Unicode keysym
	} {
		if got := km.keysym(c.r, c.group); got != c.want {
			t.Errorf("keysym(%q, %d) = %#x, want %#x", c.r, c.group, got, c.want)
		}
	}
	var none *keymap
	if got := none.keysym('é', 0); got != 0xe9 {
		t.Errorf("nil keymap keysym = %#x", got)
	}
	for _, bad := range []string{"", "xkb_keymap { };", "xkb_symbols \"x\" { name[1]=\"x\"; };"} {
		if _, err := parseKeymap(bad); err == nil {
			t.Errorf("parseKeymap(%q) succeeded", bad)
		}
	}
}

func TestKeysymFromName(t *testing.T) {
	for name, want := range map[string]rune{
		"eacute": 'é', "EuroSign": '€', "emdash": '—', "Cyrillic_a": 'а', "U20AC": '€', "0x10020ac": '€',
		"0x20ac": '€', "0x41": 'A', "space": ' ', "1": '1',
	} {
		k, ok := keysymFromName(name)
		if !ok || k.r != want {
			t.Errorf("keysymFromName(%q) = %+v, %v; want %q", name, k, ok, want)
		}
	}
	for _, name := range []string{"F8", "Control_L", "0xffc5", "KP_1", "NoSymbol", "0x1008fe01", "Uzz", "0xzz"} {
		if k, ok := keysymFromName(name); ok {
			t.Errorf("keysymFromName(%q) = %+v, want no character", name, k)
		}
	}
	if !asciiTypeable("Hello, world!\n") || asciiTypeable("café") {
		t.Error("asciiTypeable")
	}
}

func TestParseHotkey(t *testing.T) {
	for _, c := range []struct {
		in, trigger, id string
		qt              int32
	}{
		{"F8", "F8", "hotkey-f8", 0x01000037},
		{"Super+Space", "LOGO+space", "hotkey-meta-space", 0x10000020},
		{"ctrl+shift+space", "CTRL+SHIFT+space", "hotkey-ctrl-shift-space", 0x06000020},
		{"Shift+Ctrl+Space", "CTRL+SHIFT+space", "hotkey-ctrl-shift-space", 0x06000020},
		{"Alt+Pause", "ALT+Pause", "hotkey-alt-pause", 0x08000000 | 0x01000008},
		{"Meta+ScrollLock", "LOGO+Scroll_Lock", "hotkey-meta-scroll_lock", 0x10000000 | 0x01000026},
		{"Ctrl+Alt+Shift+F10", "CTRL+ALT+SHIFT+F10", "hotkey-ctrl-alt-shift-f10", 0x0e000000 | 0x01000039},
		{"Win+k", "LOGO+k", "hotkey-meta-k", 0x10000000 | 'K'},
	} {
		h, err := parseHotkey(c.in)
		if err != nil {
			t.Errorf("parseHotkey(%q): %v", c.in, err)
			continue
		}
		if h.trigger() != c.trigger || h.id() != c.id || h.qtKey() != c.qt || h.combo != c.in {
			t.Errorf("parseHotkey(%q) = trigger %q id %q qt %#x, want %q %q %#x", c.in, h.trigger(), h.id(), h.qtKey(), c.trigger, c.id, c.qt)
		}
	}
	for _, bad := range []string{"", "F8+", "Hyper+F8", "Ctrl+Nope", "Ctrl"} {
		if _, err := parseHotkey(bad); err == nil {
			t.Errorf("parseHotkey(%q) succeeded", bad)
		}
	}
	h, _ := parseHotkey("Ctrl+Shift+Space")
	for desc, want := range map[string]bool{
		"Ctrl+Shift+Space": true, "Shift+Ctrl+Space": true, "F8, Ctrl+Shift+Space": true,
		"": false, "Ctrl+Space": false, "Meta+Space\tAlt+Q": false,
	} {
		if got := sameTrigger(desc, h); got != want {
			t.Errorf("sameTrigger(%q) = %v, want %v", desc, got, want)
		}
	}
}

func TestTerminalTable(t *testing.T) {
	for app, want := range map[string]bool{
		"org.kde.konsole": true, "kitty": true, "Alacritty": true, "foot": true, "org.wezfurlong.wezterm": true,
		"org.gnome.Terminal": true, "XTerm": true, "com.mitchellh.ghostty": true, "org.kde.yakuake.desktop": true,
		"org.gnome.Ptyxis": true, "brave-browser": false, "firefox": false, "code": false, "org.kde.kate": false,
		"": false,
	} {
		if got := isTerminal(app); got != want {
			t.Errorf("isTerminal(%q) = %v, want %v", app, got, want)
		}
	}
	if k := pasteKeys("org.kde.konsole"); len(k) != 3 || k[0] != keysymControlL || k[1] != keysymShiftL || k[2] != 'v' {
		t.Errorf("terminal paste keys = %#x", k)
	}
	if k := pasteKeys("brave-browser"); len(k) != 2 || k[0] != keysymControlL || k[1] != 'v' {
		t.Errorf("paste keys = %#x", k)
	}
}

func TestDesktopEntry(t *testing.T) {
	if got := desktopExecQuote("/usr/local/bin/footman"); got != "/usr/local/bin/footman" {
		t.Errorf("plain path quoted: %q", got)
	}
	if got := desktopExecQuote(`/home/a b/$x"`); got != `"/home/a b/\\$x\\""` {
		t.Errorf("quoted = %q", got)
	}
	in := newInput(&bus{}, nil, nil, &Paths{})
	dir := t.TempDir()
	in.dataHome = func() string { return dir }
	in.tokenFile = filepath.Join(dir, "footman", "remote-desktop.token")
	files, err := in.SetupInput(context.Background(), "/opt/footman/footman")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "applications", AppID+".desktop")
	if len(files.Program) != 1 || files.Program[0] != want || len(files.State) != 1 || files.State[0] != in.tokenFile {
		t.Fatalf("files = %+v", files)
	}
	b, err := os.ReadFile(want)
	if err != nil || !strings.Contains(string(b), "Exec=/opt/footman/footman daemon\n") || !strings.Contains(string(b), "Name=Footman\n") {
		t.Errorf("desktop file = %q, %v", b, err)
	}
}

// stubWindows reports a fixed active app.
type stubWindows struct{ app string }

func (s stubWindows) Active(context.Context) (platform.Window, error) {
	if s.app == "" {
		return platform.Window{}, platform.ErrNotFound
	}
	return platform.Window{ID: "1", App: s.app}, nil
}
func (stubWindows) List(context.Context) ([]platform.Window, error) { return nil, nil }
func (stubWindows) Focus(context.Context, string) error             { return nil }

// inputRig is an Input wired to a fake portal and a fake compositor.
type inputRig struct {
	in     *Input
	portal *fakePortal
	wl     *fakeCompositor
}

func newInputRig(t *testing.T, app string, withKGA bool) *inputRig {
	t.Helper()
	addr := startBus(t)
	fp := newFakePortal(t, addr, withKGA)
	wl := newFakeCompositor(t, testKeymap)
	p := newTestPlatform(t, addr, defaultRun)
	in := p.input
	in.socket = func() string { return wl.socket }
	in.windows = stubWindows{app}
	return &inputRig{in: in, portal: fp, wl: wl}
}

// pasteOnKeys makes the fake "focused app" read the clipboard when it sees
// the paste keys, as a real app does, and returns what it got.
func (r *inputRig) pasteOnKeys(t *testing.T, mime string) func() string {
	t.Helper()
	var mu sync.Mutex
	var got string
	done := make(chan struct{})
	var once sync.Once
	ctrl := false
	r.portal.mu.Lock()
	r.portal.onKey = func(e keyEvent) {
		if e.sym == keysymControlL {
			ctrl = e.pressed
		}
		if e.sym == 'v' && e.pressed && ctrl {
			go func() {
				s, err := r.wl.appRead(mime)
				if err != nil {
					t.Errorf("app read: %v", err)
				}
				mu.Lock()
				got = s
				mu.Unlock()
				once.Do(func() { close(done) })
			}()
		}
	}
	r.portal.mu.Unlock()
	return func() string {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the app never pasted")
		}
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func syms(keys []keyEvent) []uint32 {
	var out []uint32
	for _, k := range keys {
		if k.pressed {
			out = append(out, k.sym)
		}
	}
	return out
}

func TestPaste(t *testing.T) {
	for _, c := range []struct {
		app  string
		keys []uint32
	}{
		{"brave-browser", []uint32{keysymControlL, 'v'}},
		{"org.kde.konsole", []uint32{keysymControlL, keysymShiftL, 'v'}},
		{"", []uint32{keysymControlL, 'v'}}, // focus unknown: Ctrl+V
	} {
		t.Run(c.app, func(t *testing.T) {
			r := newInputRig(t, c.app, false)
			prev := clip{{"text/plain;charset=utf-8", []byte("previous")}, {"text/html", []byte("<b>previous</b>")}}
			r.wl.copyExternal(prev)
			pasted := r.pasteOnKeys(t, "text/plain;charset=utf-8")
			const text = "Café — “dictated” 👋\nline two"
			if err := r.in.Paste(context.Background(), text); err != nil {
				t.Fatal(err)
			}
			if got := pasted(); got != text {
				t.Errorf("app pasted %q, want %q", got, text)
			}
			if got := syms(r.portal.keyLog()); !equalSyms(got, c.keys) {
				t.Errorf("keys pressed %#x, want %#x", got, c.keys)
			}
			// Every key pressed was released.
			if n := len(r.portal.keyLog()); n != 2*len(c.keys) {
				t.Errorf("%d key events, want %d", n, 2*len(c.keys))
			}
			// The previous clipboard is back, every type of it.
			own, _ := r.wl.selection()
			if len(own) != 2 || own[0] != "text/plain;charset=utf-8" || own[1] != "text/html" {
				t.Errorf("restored types = %v", own)
			}
			if got, _ := r.wl.appRead("text/html"); got != "<b>previous</b>" {
				t.Errorf("restored html = %q", got)
			}
		})
	}
}

func equalSyms(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPasteOffersPasswordHint(t *testing.T) {
	r := newInputRig(t, "kate", false)
	var offeredMu sync.Mutex
	var offered []string
	r.portal.mu.Lock()
	r.portal.onKey = func(e keyEvent) {
		if e.sym == 'v' && e.pressed {
			offeredMu.Lock()
			offered, _ = r.wl.selection()
			offeredMu.Unlock()
			go func() { _, _ = r.wl.appRead("text/plain") }()
		}
	}
	r.portal.mu.Unlock()
	if err := r.in.Paste(context.Background(), "secret-ish"); err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	offeredMu.Lock()
	defer offeredMu.Unlock()
	for _, m := range offered {
		has[m] = true
	}
	for _, m := range []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", passwordHint} {
		if !has[m] {
			t.Errorf("paste offered %v, missing %s", offered, m)
		}
	}
	// The hint's value is "secret", which Klipper checks before keeping it.
	// (Reading the hint must not count as the app reading the text.)
	if isTextMime(passwordHint) || !isTextMime("UTF8_STRING") {
		t.Error("isTextMime")
	}
	// The clipboard was empty before, so it is empty again.
	if own, ext := r.wl.selection(); own != nil || ext != nil {
		t.Errorf("clipboard after paste = %v %v, want empty", own, ext)
	}
}

func TestPasteRestoresAfterTimeout(t *testing.T) {
	r := newInputRig(t, "kate", false)
	r.wl.copyExternal(clip{{"text/plain", []byte("previous")}})
	start := time.Now()
	if err := r.in.Paste(context.Background(), "nobody reads this"); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < pasteReadTimeout {
		t.Errorf("restored after %v, before the %v timeout", el, pasteReadTimeout)
	}
	if got, _ := r.wl.appRead("text/plain"); got != "previous" {
		t.Errorf("clipboard = %q, want previous", got)
	}
}

func TestPasteLeavesNewerCopy(t *testing.T) {
	r := newInputRig(t, "kate", false)
	r.wl.copyExternal(clip{{"text/plain", []byte("previous")}})
	r.portal.mu.Lock()
	r.portal.onKey = func(e keyEvent) {
		if e.sym == 'v' && e.pressed {
			// The user copies something else before the app has pasted.
			go r.wl.copyExternal(clip{{"text/plain", []byte("newer")}})
		}
	}
	r.portal.mu.Unlock()
	if err := r.in.Paste(context.Background(), "dictated"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.wl.appRead("text/plain"); got != "newer" {
		t.Errorf("clipboard = %q, want the newer copy left alone", got)
	}
}

func TestTypeUsesLayoutKeysymsAndRate(t *testing.T) {
	r := newInputRig(t, "kate", false)
	text := strings.Repeat("ab", 50) + "€\n"
	start := time.Now()
	if err := r.in.Type(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	got := syms(r.portal.keyLog())
	if len(got) != 102 || got[0] != 'a' || got[1] != 'b' || got[100] != 0x10020ac || got[101] != keysymReturn {
		t.Fatalf("typed keysyms %#x", got)
	}
	if min := 102 * time.Second / maxCharsPerSecond; el < min*9/10 {
		t.Errorf("typed 102 characters in %v: faster than %d/s", el, maxCharsPerSecond)
	}
	// The restore token from Start is saved for next time.
	b, err := os.ReadFile(r.in.tokenFile)
	if err != nil || strings.TrimSpace(string(b)) != "token-1" {
		t.Errorf("token file = %q, %v", b, err)
	}
	if fi, err := os.Stat(r.in.tokenFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode: %v %v", fi, err)
	}
	if reg := r.portal.get(func(f *fakePortal) any { return f.registered }).([]string); len(reg) != 1 || reg[0] != AppID {
		t.Errorf("registered %v, want %s first", reg, AppID)
	}
}

func TestTypeableUsesKeymap(t *testing.T) {
	r := newInputRig(t, "kate", false)
	if !r.in.Typeable("qaz 1-4") {
		t.Error("ASCII on the layout not typeable")
	}
	if r.in.Typeable("ß") || r.in.Typeable("é") {
		t.Error("characters off the first group typeable")
	}
	if !r.in.Typeable("—") {
		t.Error("em dash on level 3 not typeable")
	}
	// Without a compositor, only ASCII counts.
	r.in.socket = func() string { return "" }
	_ = r.in.Close()
	if !r.in.Typeable("plain") || r.in.Typeable("—") {
		t.Error("fallback without a keymap")
	}
}

func TestRemoteDesktopSessionLifecycle(t *testing.T) {
	r := newInputRig(t, "kate", false)
	ctx := context.Background()
	if err := r.in.Type(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := r.in.Type(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if n := r.portal.get(func(f *fakePortal) any { return f.starts }); n != 1 {
		t.Errorf("Start called %d times, want 1 session reused", n)
	}
	// The user stops it from the tray: the next Type starts a new session
	// with the saved restore token, so no dialog.
	r.portal.closeSession(r.portal.get(func(f *fakePortal) any { return f.sessions[0] }).(dbus.ObjectPath))
	time.Sleep(100 * time.Millisecond)
	if err := r.in.Type(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	r.portal.mu.Lock()
	starts, sel := r.portal.starts, r.portal.selectOpts
	r.portal.mu.Unlock()
	if starts != 2 {
		t.Fatalf("Start called %d times, want 2", starts)
	}
	if _, ok := sel[0]["restore_token"]; ok {
		t.Error("first session sent a restore token")
	}
	if tok, _ := sel[1]["restore_token"].Value().(string); tok != "token-1" {
		t.Errorf("second session token = %q, want token-1", tok)
	}
	if pm, _ := sel[1]["persist_mode"].Value().(uint32); pm != 2 {
		t.Errorf("persist_mode = %d, want 2", pm)
	}
	if types, _ := sel[1]["types"].Value().(uint32); types != 1 {
		t.Errorf("device types = %d, want keyboard only", types)
	}
}

func TestTypeDenied(t *testing.T) {
	r := newInputRig(t, "kate", false)
	r.portal.set(func(f *fakePortal) { f.denyStart = true })
	err := r.in.Type(context.Background(), "x")
	if !errors.Is(err, errPortalCancelled) {
		t.Errorf("err = %v, want a cancelled-dialog error", err)
	}
	if len(r.portal.keyLog()) != 0 {
		t.Error("keys sent without permission")
	}
}

func TestHotkeysPortal(t *testing.T) {
	r := newInputRig(t, "", false)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := r.in.Hotkeys(ctx, []string{"F8", "Ctrl+Shift+Space", "f8"})
	if err != nil {
		t.Fatal(err)
	}
	bound := r.portal.get(func(f *fakePortal) any { return f.bound[0] }).([]gsShortcut)
	if len(bound) != 2 || bound[0].ID != "hotkey-f8" || bound[1].ID != "hotkey-ctrl-shift-space" {
		t.Fatalf("bound %+v", bound)
	}
	if pt, _ := bound[1].Opts["preferred_trigger"].Value().(string); pt != "CTRL+SHIFT+space" {
		t.Errorf("preferred_trigger = %q", pt)
	}
	r.portal.shortcut("hotkey-ctrl-shift-space", true)
	r.portal.shortcut("unknown", true)
	r.portal.shortcut("hotkey-ctrl-shift-space", false)
	for _, want := range []platform.HotkeyEvent{{Hotkey: "Ctrl+Shift+Space", Pressed: true}, {Hotkey: "Ctrl+Shift+Space"}} {
		select {
		case ev := <-ch:
			if ev.Hotkey != want.Hotkey || ev.Pressed != want.Pressed || ev.At.IsZero() {
				t.Errorf("event %+v, want %+v", ev, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no hotkey event")
		}
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("unexpected event after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed on cancel")
	}
	if _, err := r.in.Hotkeys(context.Background(), []string{"Hyper+X"}); err == nil {
		t.Error("bad combination accepted")
	}
}

func TestBindHotkeysForcesKey(t *testing.T) {
	r := newInputRig(t, "", true)
	// KDE kept an empty key from a dismissed dialog.
	r.portal.set(func(f *fakePortal) { f.gsTriggers["hotkey-f8"] = "" })
	got, err := r.in.BindHotkeys(context.Background(), []string{"F8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hotkey != "F8" || got[0].Trigger != "F8" {
		t.Errorf("BindHotkeys = %+v", got)
	}
	if k := r.portal.get(func(f *fakePortal) any { return f.foreign["hotkey-f8"] }).([]int32); len(k) != 4 || k[0] != 0x01000037 {
		t.Errorf("setForeignShortcutKeys got %#x", k)
	}
	// Already on the right key: left alone.
	r.portal.set(func(f *fakePortal) { f.foreign = map[string][]int32{} })
	if _, err := r.in.BindHotkeys(context.Background(), []string{"F8"}); err != nil {
		t.Fatal(err)
	}
	if n := r.portal.get(func(f *fakePortal) any { return len(f.foreign) }); n != 0 {
		t.Error("rebound a shortcut already on its key")
	}
	// A key another app uses is refused with its name.
	r.portal.set(func(f *fakePortal) {
		f.kgaTaken[0x01000038] = "Expose"
		f.gsTriggers["hotkey-f9"] = "F12"
	})
	if _, err := r.in.BindHotkeys(context.Background(), []string{"F9"}); err == nil || !strings.Contains(err.Error(), "Expose") {
		t.Errorf("taken key: err = %v", err)
	}
}

func TestBindHotkeysOpensDialogWithoutKDE(t *testing.T) {
	r := newInputRig(t, "", false)
	r.portal.set(func(f *fakePortal) { f.gsTriggers["hotkey-f8"] = "F12" })
	got, err := r.in.BindHotkeys(context.Background(), []string{"F8"})
	if err != nil {
		t.Fatal(err)
	}
	if n := r.portal.get(func(f *fakePortal) any { return f.configured }); n != 1 {
		t.Errorf("ConfigureShortcuts called %d times, want 1", n)
	}
	if got[0].Trigger != "F12" {
		t.Errorf("trigger = %q, want what the desktop reports", got[0].Trigger)
	}
}

func TestHotkeysKGlobalAccelFallback(t *testing.T) {
	r := newInputRig(t, "", true)
	r.portal.set(func(f *fakePortal) { f.gsVersion = 0 }) // a portal without GlobalShortcuts
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := r.in.Hotkeys(ctx, []string{"Pause"})
	if err != nil {
		t.Fatal(err)
	}
	if k := r.portal.get(func(f *fakePortal) any { return f.kgaKeys["hotkey-pause"] }).(int32); k != 0x01000008 {
		t.Errorf("kglobalaccel key = %#x", k)
	}
	emit := func(name string) {
		_ = r.portal.kgaConn.Emit(fakeKGAComp, kglobalaccelComp+"."+name, AppID, "hotkey-pause", int64(1))
	}
	emit("globalShortcutPressed")
	emit("globalShortcutReleased")
	for _, want := range []bool{true, false} {
		select {
		case ev := <-ch:
			if ev.Hotkey != "Pause" || ev.Pressed != want {
				t.Errorf("event %+v", ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no event")
		}
	}
	cancel()
	for range ch {
	}
	r.portal.mu.Lock()
	inactive := r.portal.inactive
	r.portal.mu.Unlock()
	if len(inactive) != 1 || inactive[0] != "hotkey-pause" {
		t.Errorf("setInactive on exit: %v", inactive)
	}
	// A key another component holds is refused, not taken.
	r.portal.set(func(f *fakePortal) { f.kgaTaken[0x08000000|0x01000033] = "Close Window" })
	if _, err := r.in.Hotkeys(context.Background(), []string{"Alt+F4"}); err == nil || !strings.Contains(err.Error(), "Close Window") {
		t.Errorf("taken key: err = %v", err)
	}
}

func TestHotkeysUnsupported(t *testing.T) {
	r := newInputRig(t, "", false)
	r.portal.set(func(f *fakePortal) { f.gsVersion = 0 })
	if _, err := r.in.Hotkeys(context.Background(), []string{"F8"}); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestPasteWithoutWayland(t *testing.T) {
	r := newInputRig(t, "", false)
	r.in.socket = func() string { return "" }
	if err := r.in.Paste(context.Background(), "x"); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}
