//go:build linux && integration

package linux

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// The TestNested* tests drive Input inside a private, headless KWin
// (testdata/nested-kwin.sh): its own Wayland socket, D-Bus session bus,
// portals, kglobalaccel, permission store and XDG folders. Nothing reaches
// the real desktop: every connection the tests make is to the nested
// session's addresses, never to $WAYLAND_DISPLAY or the session bus. See
// README.md ("Nested input tests").
//
//	go test -tags integration -v -count=1 ./internal/platform/linux/ -run TestNested

var (
	nestedOnce sync.Once
	nestedSess *nestedSession
	nestedErr  error
)

// nestedSession is the shared Nested session (nested.go) with test helpers.
type nestedSession struct{ *Nested }

func TestMain(m *testing.M) {
	code := m.Run()
	if nestedSess != nil {
		nestedSess.Stop()
	}
	os.Exit(code)
}

// nested returns the shared nested session, starting it on first use, or
// skips the test if KWin is not installed.
func nested(t *testing.T) *nestedSession {
	t.Helper()
	for _, tool := range NestedTools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; the nested KWin tests need KDE Plasma's KWin", tool)
		}
	}
	nestedOnce.Do(func() {
		var n *Nested
		n, nestedErr = StartNested()
		if nestedErr == nil {
			nestedSess = &nestedSession{n}
		}
	})
	if nestedErr != nil {
		t.Fatal(nestedErr)
	}
	return nestedSess
}

func (n *nestedSession) environ() []string { return n.Environ() }

// platform returns a Platform wired only to the nested session.
func (n *nestedSession) platform(t *testing.T) *Platform {
	t.Helper()
	p := n.NewPlatform(filepath.Join(n.Root, "footman-data", "remote-desktop.token"))
	t.Cleanup(func() { _ = p.Close() })
	// The portal only accepts the app id if the .desktop file's program
	// exists, so point it at this test binary.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.input.SetupInput(context.Background(), exe); err != nil {
		t.Fatal(err)
	}
	if err := n.AllowRemoteDesktop(); err != nil {
		t.Fatalf("pre-approving remote desktop: %v", err)
	}
	return p
}

// start runs a program inside the nested session until the test ends.
func (n *nestedSession) start(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = n.environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logf, err := os.Create(filepath.Join(t.TempDir(), filepath.Base(name)+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		_ = logf.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			t.Logf("%s output:\n%s", name, tail(string(b), 3000))
		}
	})
	return cmd
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// sinkLog follows a JSON-lines log written by a text sink.
type sinkLog struct{ path string }

func (s sinkLog) last() (map[string]any, bool) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	var last map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			last = m
		}
	}
	return last, last != nil
}

// waitFor polls the sink until cond holds for its last record.
func (s sinkLog) waitFor(t *testing.T, what string, d time.Duration, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	var m map[string]any
	for time.Now().Before(deadline) {
		if m, _ = s.last(); m != nil && cond(m) {
			return m
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last record: %v", what, m)
	return nil
}

func textIs(want string) func(map[string]any) bool {
	return func(m map[string]any) bool { s, _ := m["text"].(string); return s == want }
}

func ready(m map[string]any) bool { r, _ := m["ready"].(bool); return r }

// waitActive waits until the active window's app id contains want.
func waitActive(t *testing.T, p *Platform, want string) platform.Window {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last platform.Window
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		last, lastErr = p.Windows().Active(ctx)
		cancel()
		if lastErr == nil && strings.Contains(strings.ToLower(last.App), strings.ToLower(want)) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("window %q never became active; last: %+v, %v", want, last, lastErr)
	return last
}

// nestedClipboard reads the nested session's clipboard with wl-paste.
func (n *nestedSession) clipboard(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("wl-paste", "--no-newline")
	cmd.Env = n.environ()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// setClipboard sets the nested session's clipboard with wl-copy, which keeps
// serving it from a background process (another app owning the clipboard).
func (n *nestedSession) setClipboard(t *testing.T, text string) {
	t.Helper()
	cmd := exec.Command("wl-copy", "--type", "text/plain;charset=utf-8")
	cmd.Env = n.environ()
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		t.Fatalf("wl-copy: %v", err)
	}
}

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

func needPython(t *testing.T, module string) {
	t.Helper()
	need(t, "python3")
	if err := exec.Command("python3", "-c", "import "+module).Run(); err != nil {
		t.Skipf("python3 module %s is not installed", module)
	}
}

// startQtSink opens the Qt text sink and waits until it has focus.
func startQtSink(t *testing.T, n *nestedSession, p *Platform) sinkLog {
	t.Helper()
	needPython(t, "PyQt6")
	sink := sinkLog{filepath.Join(t.TempDir(), "qt.jsonl")}
	script, _ := filepath.Abs("testdata/qtsink.py")
	n.start(t, "python3", script, sink.path)
	sink.waitFor(t, "the Qt sink", 20*time.Second, ready)
	waitActive(t, p, "qtsink")
	return sink
}

const (
	asciiSample   = `The quick brown fox jumps over the lazy dog: 0123456789 !"#$%&'()*+,-./:;<=>?@[\]^_{|}~`
	unicodeSample = "Café déjà vu — naïve “quotes” ‘single’ … £20 €5 ½ Straße αβγ Привет 日本語 한국어 👋 ✓"
)

func TestNestedKeymap(t *testing.T) {
	n := nested(t)
	p := n.platform(t)
	in := p.input
	ctx := context.Background()
	// A keymap only reaches clients once the seat has a keyboard, which a
	// headless KWin gets with the portal session's virtual keyboard.
	if _, _, err := in.remote.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	km, group := in.layout(ctx)
	if km == nil {
		t.Fatal("no keymap from the compositor")
	}
	t.Logf("keymap: %d group(s), active %d, %d characters in group 1", len(km.groups), group, len(km.groups[0]))
	for _, c := range []struct {
		text string
		want bool
	}{
		{asciiSample, true},
		{"two\nlines\tand a tab", true},
		{"café", false},
		{"—", false},
		{"👋", false},
	} {
		if got := in.Typeable(c.text); got != c.want {
			t.Errorf("Typeable(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestNestedTypeQt(t *testing.T) {
	n := nested(t)
	p := n.platform(t)
	sink := startQtSink(t, n, p)
	ctx := context.Background()
	start := time.Now()
	if err := p.Input().Type(ctx, asciiSample); err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	sink.waitFor(t, "typed text", 10*time.Second, textIs(asciiSample))
	cps := float64(len(asciiSample)) / el.Seconds()
	t.Logf("typed %d characters in %v (%.0f chars/s)", len(asciiSample), el.Round(time.Millisecond), cps)
	if cps > maxCharsPerSecond*1.05 {
		t.Errorf("typing ran at %.0f chars/s, over the %d cap", cps, maxCharsPerSecond)
	}
}

func TestNestedPasteQt(t *testing.T) {
	need(t, "wl-copy", "wl-paste")
	n := nested(t)
	p := n.platform(t)
	sink := startQtSink(t, n, p)
	n.setClipboard(t, "previous clipboard ✓")
	if err := p.Input().Paste(context.Background(), unicodeSample); err != nil {
		t.Fatal(err)
	}
	sink.waitFor(t, "pasted text", 5*time.Second, textIs(unicodeSample))
	if got := n.clipboard(t); got != "previous clipboard ✓" {
		t.Errorf("clipboard after paste = %q, want the previous contents back", got)
	}
}

func TestNestedPasteRestoresEmptyClipboard(t *testing.T) {
	need(t, "wl-copy", "wl-paste")
	n := nested(t)
	p := n.platform(t)
	sink := startQtSink(t, n, p)
	cmd := exec.Command("wl-copy", "--clear")
	cmd.Env = n.environ()
	_ = cmd.Run()
	if err := p.Input().Paste(context.Background(), "two\nlines"); err != nil {
		t.Fatal(err)
	}
	sink.waitFor(t, "pasted text", 5*time.Second, textIs("two\nlines"))
	if got := n.clipboard(t); got != "" {
		t.Errorf("clipboard after paste = %q, want it empty again", got)
	}
}

func TestNestedPasteKonsole(t *testing.T) {
	need(t, "konsole", "wl-copy", "wl-paste")
	needPython(t, "termios")
	n := nested(t)
	p := n.platform(t)
	sink := sinkLog{filepath.Join(t.TempDir(), "tty.jsonl")}
	script, _ := filepath.Abs("testdata/ttysink.py")
	n.start(t, "konsole", "--separate", "--nofork", "-e", "python3", script, sink.path)
	sink.waitFor(t, "the terminal sink", 30*time.Second, ready)
	w := waitActive(t, p, "konsole")
	if !isTerminal(w.App) {
		t.Fatalf("Konsole's app id %q is not in the terminal table", w.App)
	}
	n.setClipboard(t, "previous")
	text := "echo dictated — not run\nsecond line"
	if err := p.Input().Paste(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	// Terminals turn pasted newlines into carriage returns, as for typed Enter;
	// the bracketing is what stops the shell from running them.
	m := sink.waitFor(t, "pasted text", 5*time.Second, textIs(strings.ReplaceAll(text, "\n", "\r")))
	if raw, _ := m["raw"].(string); !strings.HasPrefix(raw, "\x1b[200~") {
		t.Errorf("paste was not bracketed (so a shell would run it): %q", raw)
	}
	if got := n.clipboard(t); got != "previous" {
		t.Errorf("clipboard after paste = %q, want %q", got, "previous")
	}
}

// chromium finds a Chromium-based browser: $FOOTMAN_TEST_CHROMIUM, Brave,
// Chromium or Chrome.
func chromium(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("FOOTMAN_TEST_CHROMIUM"); b != "" {
		return b
	}
	for _, b := range []string{"/opt/brave.com/brave/brave", "brave-browser", "chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	t.Skip("no Chromium-based browser found; set FOOTMAN_TEST_CHROMIUM")
	return ""
}

// webSink serves a page with a focused textarea that reports its value.
type webSink struct {
	mu   sync.Mutex
	text string
	seen bool
	url  string
}

const webSinkPage = `<!doctype html><meta charset=utf-8><title>websink</title>
<textarea id=t autofocus style="width:95vw;height:90vh"></textarea>
<script>
const t = document.getElementById('t');
function send(){ fetch('/log', {method:'POST', body: t.value}); }
t.addEventListener('input', send);
window.addEventListener('focus', () => { t.focus(); });
t.focus();
fetch('/ready', {method:'POST'});
</script>`

func startWebSink(t *testing.T) *webSink {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w := &webSink{url: "http://" + l.Addr().String() + "/"}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(rw, webSinkPage) })
	mux.HandleFunc("/ready", func(http.ResponseWriter, *http.Request) {
		w.mu.Lock()
		w.seen = true
		w.mu.Unlock()
	})
	mux.HandleFunc("/log", func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.text = string(b)
		w.mu.Unlock()
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return w
}

func (w *webSink) wait(t *testing.T, what string, d time.Duration, cond func(text string, seen bool) bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		ok := cond(w.text, w.seen)
		w.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	t.Fatalf("timed out waiting for %s; page text %q", what, w.text)
}

// TestNestedChromium answers the open question from the spike (S0-06): Brave
// ignored paste keys sent through KWin's fake_input, so does it accept them
// from the RemoteDesktop portal?
func TestNestedChromium(t *testing.T) {
	browser := chromium(t)
	n := nested(t)
	p := n.platform(t)
	ctx := context.Background()
	// Chromium ignores keys unless the seat had a keyboard when it started;
	// the portal session's virtual keyboard is that keyboard.
	if _, _, err := p.input.remote.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	web := startWebSink(t)
	n.start(t, browser, "--user-data-dir="+t.TempDir(), "--ozone-platform=wayland", "--no-first-run",
		"--no-default-browser-check", "--password-store=basic", "--disable-gpu", "--disable-features=Translate",
		"--app="+web.url)
	web.wait(t, "the page to load", 60*time.Second, func(_ string, seen bool) bool { return seen })
	w := waitActive(t, p, "")
	t.Logf("browser window: app=%q title=%q", w.App, w.Title)
	time.Sleep(500 * time.Millisecond)

	t.Run("paste Ctrl+V", func(t *testing.T) {
		n.setClipboard(t, "previous")
		if err := p.Input().Paste(ctx, unicodeSample); err != nil {
			t.Fatal(err)
		}
		web.wait(t, "pasted text", 5*time.Second, func(s string, _ bool) bool { return s == unicodeSample })
		if got := n.clipboard(t); got != "previous" {
			t.Errorf("clipboard after paste = %q, want %q", got, "previous")
		}
	})
	t.Run("type", func(t *testing.T) {
		const typed = " and typed text."
		if err := p.Input().Type(ctx, typed); err != nil {
			t.Fatal(err)
		}
		web.wait(t, "typed text", 5*time.Second, func(s string, _ bool) bool { return s == unicodeSample+typed })
	})
}

// pressKey sends a key press or release through the portal session, the
// same path a physical key takes into KWin's shortcut handling.
func pressKey(t *testing.T, in *Input, sym uint32, state uint32) {
	t.Helper()
	ctx := context.Background()
	p, s, err := in.remote.ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.remote.keysym(ctx, p, s, sym, state); err != nil {
		t.Fatal(err)
	}
}

func expectHotkey(t *testing.T, ch <-chan platform.HotkeyEvent, combo string, pressed bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("hotkey channel closed")
		}
		if ev.Hotkey != combo || ev.Pressed != pressed {
			t.Fatalf("got %+v, want %s pressed=%v", ev, combo, pressed)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s event (pressed=%v)", combo, pressed)
	}
}

const (
	keysymF6  = 0xffc3
	keysymF8  = KeysymF8
	keysymF12 = 0xffc9
)

// tapHotkey presses and releases sym and expects combo's events.
func tapHotkey(t *testing.T, in *Input, ch <-chan platform.HotkeyEvent, sym uint32, combo string) {
	t.Helper()
	pressKey(t, in, sym, keyPressed)
	expectHotkey(t, ch, combo, true)
	pressKey(t, in, sym, keyReleased)
	expectHotkey(t, ch, combo, false)
}

func TestNestedHotkeysPortal(t *testing.T) {
	n := nested(t)
	p := n.platform(t)
	in := p.input
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// A dismissed dialog leaves KDE with a shortcut that has no key, and
	// binding again does not ask again: BindHotkeys must still end up on F6.
	dctx, dcancel := context.WithTimeout(ctx, 4*time.Second)
	if _, err := in.BindHotkeys(dctx, []string{"F6"}); err == nil {
		t.Log("the shortcut dialog did not appear for F6 (already bound in this session?)")
	}
	dcancel()
	approveShortcutDialog(t, n, p)
	got, err := in.BindHotkeys(ctx, []string{"F6"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Trigger != "F6" {
		t.Fatalf("BindHotkeys after a dismissed dialog = %+v, want F6", got)
	}

	// A fresh shortcut: the dialog is approved and the key works.
	got, err = in.BindHotkeys(ctx, []string{"F8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Trigger != "F8" {
		t.Fatalf("BindHotkeys = %+v, want F8", got)
	}
	// Through the portal (as on other desktops), then through KGlobalAccel
	// (as the daemon does on KDE) below.
	hctx, stop := context.WithCancel(ctx)
	in.forcePortal = true
	ch, err := in.Hotkeys(hctx, []string{"F8", "F6"})
	in.forcePortal = false
	if err != nil {
		t.Fatal(err)
	}
	tapHotkey(t, in, ch, keysymF8, "F8")
	tapHotkey(t, in, ch, keysymF6, "F6")
	stop()
	for range ch {
	}

	// The user moves Footman's F8 shortcut to F12 in System Settings: events
	// still name "F8", the combination Footman asked for.
	_, ka, err := in.kglobalaccel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f12, _ := parseHotkey("F12")
	f8, _ := parseHotkey("F8")
	if err := ka.Call(kglobalaccelIface+".setForeignShortcutKeys", 0, in.kgaActionID(f8), []kgaKeys{{[]int32{f12.qtKey(), 0, 0, 0}}}).Err; err != nil {
		t.Fatal(err)
	}
	hctx, stop = context.WithCancel(ctx)
	ch, err = in.Hotkeys(hctx, []string{"F8"})
	if err != nil {
		t.Fatal(err)
	}
	tapHotkey(t, in, ch, keysymF12, "F8")
	stop()
	for range ch {
	}

	// Rebinding through setup puts it back on F8.
	got, err = in.BindHotkeys(ctx, []string{"F8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Trigger != "F8" {
		t.Fatalf("rebind = %+v, want F8", got)
	}
	ch, err = in.Hotkeys(ctx, []string{"F8"})
	if err != nil {
		t.Fatal(err)
	}
	tapHotkey(t, in, ch, keysymF8, "F8")
}

// approveShortcutDialog answers KDE's "Global Shortcuts Requested" dialog by
// pressing Return in it whenever it appears during the test.
func approveShortcutDialog(t *testing.T, _ *nestedSession, p *Platform) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := p.ApproveShortcutDialogs(ctx)
	t.Cleanup(func() { cancel(); <-done })
}

func TestNestedHotkeysKGlobalAccel(t *testing.T) {
	n := nested(t)
	p := n.platform(t)
	in := p.input
	in.forceKGlobalAccel = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const combo = "Ctrl+Alt+Shift+F10"
	ch, err := in.Hotkeys(ctx, []string{combo})
	if err != nil {
		t.Fatal(err)
	}
	keys := []uint32{keysymControlL, 0xffe9 /* Alt_L */, keysymShiftL, 0xffc7 /* F10 */}
	for _, k := range keys {
		pressKey(t, in, k, keyPressed)
	}
	expectHotkey(t, ch, combo, true)
	for i := len(keys) - 1; i >= 0; i-- {
		pressKey(t, in, keys[i], keyReleased)
	}
	expectHotkey(t, ch, combo, false)
	cancel()
	for range ch {
	}

	// Another app's key is refused rather than taken.
	_, err = in.Hotkeys(context.Background(), []string{"Alt+F4"})
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("binding KWin's Alt+F4: err = %v, want 'already used'", err)
	}
}

// TestNestedHotkeysNoDialogAfterSetup is how the daemon starts at login on
// KDE: setup has bound the key through the portal (answering its dialog),
// and Hotkeys then registers without any dialog, since KDE's portal would
// ask again for every new session (ADR-0030). A key the user moved in System
// Settings stays where they put it.
func TestNestedHotkeysNoDialogAfterSetup(t *testing.T) {
	n := nested(t)
	p := n.platform(t)
	in := p.input
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	actx, stopApprover := context.WithCancel(ctx)
	approved := p.ApproveShortcutDialogs(actx)
	got, err := in.BindHotkeys(ctx, []string{"F7", "Super+Shift+F9"})
	stopApprover()
	<-approved
	if err != nil {
		t.Fatal(err)
	}
	// Super and Meta are the same key: the binding reports what was asked.
	if len(got) != 2 || got[0].Trigger != "F7" || got[1].Trigger != "Super+Shift+F9" {
		t.Fatalf("BindHotkeys = %+v", got)
	}

	// No one answers dialogs from here on.
	hctx, stop := context.WithTimeout(ctx, 10*time.Second)
	ch, err := in.Hotkeys(hctx, []string{"F7", "Super+Shift+F9"})
	if err != nil {
		t.Fatalf("Hotkeys after setup (a dialog nobody answered?): %v", err)
	}
	tapHotkey(t, in, ch, 0xffc4 /* F7 */, "F7")
	stop()
	for range ch {
	}

	// The user moves F7 to F11; the next start keeps F11.
	_, ka, err := in.kglobalaccel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f7, _ := parseHotkey("F7")
	f11, _ := parseHotkey("F11")
	if err := ka.Call(kglobalaccelIface+".setForeignShortcutKeys", 0, in.kgaActionID(f7), []kgaKeys{{[]int32{f11.qtKey(), 0, 0, 0}}}).Err; err != nil {
		t.Fatal(err)
	}
	hctx, stop = context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	ch, err = in.Hotkeys(hctx, []string{"F7"})
	if err != nil {
		t.Fatal(err)
	}
	tapHotkey(t, in, ch, 0xffc8 /* F11 */, "F7")
}
