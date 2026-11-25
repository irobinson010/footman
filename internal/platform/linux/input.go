//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// AppID is Footman's desktop application id. The .desktop file setup
// installs is named after it, and portal permissions and shortcuts are
// stored under it.
const AppID = "io.github.irobinson010.Footman"

// Input timing (ADR-0022).
const (
	// maxCharsPerSecond caps typing speed: faster bursts reorder characters
	// in Firefox.
	maxCharsPerSecond = 1000
	// pasteSettle is how long a new clipboard is left alone before the
	// paste keys, so that clipboard managers reading it are not mistaken
	// for the app.
	pasteSettle = 40 * time.Millisecond
	// pasteReadTimeout is how long to wait for the app to read the
	// clipboard before restoring it anyway.
	pasteReadTimeout = 1500 * time.Millisecond
	// pasteQuiet is how long reads must stop before restoring, since an
	// app may read several types.
	pasteQuiet = 150 * time.Millisecond
	// keymapWait bounds waiting for the compositor's first keymap.
	keymapWait = time.Second
)

// Input implements platform.Input on Wayland (ADR-0022):
//   - hotkeys through the XDG GlobalShortcuts portal, falling back to KDE's
//     KGlobalAccel;
//   - typing as keysyms through a RemoteDesktop portal session, at most
//     1,000 characters a second;
//   - pasting by offering the text on the clipboard through the Wayland
//     data-control protocol, pressing the app's paste keys through the same
//     portal session (Ctrl+Shift+V in terminals, Ctrl+V elsewhere), then
//     restoring the previous clipboard once the app has read it.
//
// It also reports whether text can be typed on the current keyboard layout
// (dictation.Layout), from the keymap the compositor sends.
type Input struct {
	appID     string
	dial      func(...dbus.ConnOption) (*dbus.Conn, error)
	bus       *bus // the platform's shared connection, for the KDE layout index
	windows   platform.Windows
	socket    func() string // Wayland socket path
	dataHome  func() string // XDG_DATA_HOME
	tokenFile string

	// forceKGlobalAccel skips the portal for hotkeys (tests).
	forceKGlobalAccel bool
	// pasteKeys, if set, picks the paste keys instead of the terminal table
	// (tests).
	pasteKeys func(app string) []uint32

	mu     sync.Mutex
	pc     *portalConn
	wl     *wlConn
	dc     *dataControl
	kb     *keyboardWatch
	remote *remoteDesktop

	insertMu sync.Mutex // one Type or Paste at a time
}

var _ platform.Input = (*Input)(nil)
var _ platform.InputSetup = (*Input)(nil)

func newInput(b *bus, dial func(...dbus.ConnOption) (*dbus.Conn, error), w platform.Windows, paths *Paths) *Input {
	in := &Input{
		appID:     AppID,
		dial:      dial,
		bus:       b,
		windows:   w,
		socket:    waylandSocket,
		dataHome:  func() string { return xdgDir("XDG_DATA_HOME", ".local/share") },
		tokenFile: filepath.Join(paths.Data(), "remote-desktop.token"),
	}
	in.remote = &remoteDesktop{portal: in.portal, tokenFile: in.tokenFile}
	return in
}

// portal returns the private portal connection, opening it on first use.
func (in *Input) portal() (*portalConn, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.pc != nil && in.pc.conn.Connected() {
		return in.pc, nil
	}
	p, err := openPortal(in.dial, in.appID)
	if err != nil {
		return nil, err
	}
	in.pc = p
	return p, nil
}

// wayland returns the compositor connection with its data-control device and
// keyboard watch, connecting on first use.
func (in *Input) wayland(ctx context.Context) (*dataControl, *keyboardWatch, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.wl != nil && in.wl.failed() == nil && (in.dc == nil || in.dc.usable()) {
		return in.dc, in.kb, nil
	}
	if in.wl != nil {
		in.wl.close()
	}
	in.wl, in.dc, in.kb = nil, nil, nil
	w, err := dialWayland(ctx, in.socket())
	if err != nil {
		return nil, nil, fmt.Errorf("linux input: %w: %w", platform.ErrUnsupported, err)
	}
	in.wl = w
	if kb, err := watchKeyboard(w); err == nil {
		in.kb = kb
	}
	dc, err := newDataControl(ctx, w)
	if err == nil {
		in.dc = dc
	}
	return in.dc, in.kb, nil
}

// Close ends the portal session and the compositor connection.
func (in *Input) Close() error {
	in.remote.close()
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.wl != nil {
		in.wl.close()
		in.wl, in.dc, in.kb = nil, nil, nil
	}
	if in.pc != nil {
		err := in.pc.close()
		in.pc = nil
		return err
	}
	return nil
}

// layout returns the current keymap and active layout group, or nil if the
// keymap is unknown.
func (in *Input) layout(ctx context.Context) (*keymap, int) {
	_, kb, err := in.wayland(ctx)
	if err != nil || kb == nil {
		return nil, 0
	}
	km, err := kb.current(ctx, keymapWait)
	if err != nil || km == nil {
		return nil, 0
	}
	return km, in.layoutGroup(ctx)
}

// layoutGroup asks KDE which layout is active (the keymap holds all of them
// as groups). Elsewhere, or if KDE does not answer, the first is assumed.
func (in *Input) layoutGroup(ctx context.Context) int {
	c, err := in.bus.get()
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	var idx uint32
	if err := c.Object("org.kde.keyboard", "/Layouts").CallWithContext(ctx, "org.kde.KeyboardLayouts.getLayout", 0).Store(&idx); err != nil {
		return 0
	}
	return int(idx)
}

// Typeable reports whether every character of text is on the active keyboard
// layout, so typing it gives exactly that text in every app (dictation.Layout).
// Chromium, Electron and Xwayland apps drop typed characters that are not on
// the layout (ADR-0022). Without a keymap, only printable ASCII counts.
func (in *Input) Typeable(text string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	km, group := in.layout(ctx)
	if km == nil {
		return asciiTypeable(text)
	}
	return km.typeable(text, group)
}

// Type types text at the cursor as keysyms through the RemoteDesktop portal.
// The first call may show the desktop's permission dialog.
func (in *Input) Type(ctx context.Context, text string) error {
	in.insertMu.Lock()
	defer in.insertMu.Unlock()
	p, s, err := in.remote.ensure(ctx)
	if err != nil {
		return err
	}
	km, group := in.layout(ctx)
	interval := time.Second / maxCharsPerSecond
	next := time.Now()
	for _, r := range text {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := in.remote.tap(ctx, p, s, km.keysym(r, group)); err != nil {
			return err
		}
		next = next.Add(interval)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		} else {
			next = time.Now()
		}
	}
	return nil
}

// Paste inserts text through the clipboard: it offers the text (marked so
// clipboard history skips it), presses the focused app's paste keys through
// the RemoteDesktop portal, waits for the app to read the clipboard (or a
// timeout), then puts back what was there before. If something else takes
// the clipboard meanwhile, that is left alone.
func (in *Input) Paste(ctx context.Context, text string) error {
	in.insertMu.Lock()
	defer in.insertMu.Unlock()
	// The portal session first: its permission dialog, if any, takes focus,
	// and focus returns before the keys are sent.
	p, s, err := in.remote.ensure(ctx)
	if err != nil {
		return err
	}
	dc, _, err := in.wayland(ctx)
	if err != nil {
		return err
	}
	if dc == nil {
		return fmt.Errorf("linux input: %w: the compositor has no data-control clipboard protocol", platform.ErrUnsupported)
	}
	keys := in.pasteKeysFor(ctx)

	prev, snapErr := dc.snapshot(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	reads := make(chan struct{}, 64)
	var armed atomic.Bool // reads before the paste keys are clipboard managers
	src, err := dc.set(ctx, textClip(text), func(mime string) {
		if armed.Load() && isTextMime(mime) {
			select {
			case reads <- struct{}{}:
			default:
			}
		}
	})
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	restore := func() error {
		if src != nil && src.isCancelled() {
			return nil // the user (or an app) has copied something since
		}
		if snapErr != nil {
			prev = nil // could not save it: at least do not leave the text behind
		}
		_, err := dc.set(rctx, prev, nil)
		return err
	}
	if err != nil {
		return errors.Join(fmt.Errorf("linux input: setting the clipboard: %w", err), restore())
	}
	if err := sleepCtx(ctx, pasteSettle); err != nil {
		return errors.Join(err, restore())
	}
	armed.Store(true)
	if err := in.remote.combo(ctx, p, s, keys); err != nil {
		return errors.Join(err, restore())
	}

	// Wait for the app to read, then for the reads to stop.
	timeout := time.NewTimer(pasteReadTimeout)
	defer timeout.Stop()
	select {
	case <-reads:
		for quiet := false; !quiet; {
			select {
			case <-reads:
			case <-time.After(pasteQuiet):
				quiet = true
			case <-src.gone:
				quiet = true
			}
		}
	case <-timeout.C:
	case <-src.gone:
	case <-ctx.Done():
	}
	if err := restore(); err != nil {
		return fmt.Errorf("linux input: restoring the clipboard: %w", err)
	}
	return ctx.Err()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pasteKeysFor returns the paste keys for the focused app: Ctrl+Shift+V in
// terminals (where Ctrl+V is a literal-next key), Ctrl+V elsewhere.
func (in *Input) pasteKeysFor(ctx context.Context) []uint32 {
	app := ""
	if in.windows != nil {
		wctx, cancel := context.WithTimeout(ctx, time.Second)
		if w, err := in.windows.Active(wctx); err == nil {
			app = w.App
		}
		cancel()
	}
	if in.pasteKeys != nil {
		return in.pasteKeys(app)
	}
	return pasteKeys(app)
}

func pasteKeys(app string) []uint32 {
	if isTerminal(app) {
		return []uint32{keysymControlL, keysymShiftL, 'v'}
	}
	return []uint32{keysymControlL, 'v'}
}

// terminalApps are terminal emulators by app id or window class, lower case:
// either the whole id or its last dot-separated part.
var terminalApps = map[string]bool{
	"konsole": true, "yakuake": true, "kitty": true, "alacritty": true, "foot": true, "footclient": true,
	"wezterm": true, "wezterm-gui": true, "gnome-terminal": true, "gnome-terminal-server": true, "terminal": true,
	"console": true, "kgx": true, "ptyxis": true, "xterm": true, "uxterm": true, "urxvt": true, "rxvt": true,
	"st": true, "st-256color": true, "terminator": true, "tilix": true, "xfce4-terminal": true, "lxterminal": true,
	"qterminal": true, "mate-terminal": true, "terminology": true, "ghostty": true, "blackbox": true,
	"cool-retro-term": true, "deepin-terminal": true, "rio": true, "warp": true, "contour": true, "tabby": true,
	"hyper": true, "sakura": true, "guake": true, "tilda": true, "terminix": true, "zutty": true, "eterm": true,
}

// isTerminal reports whether app (an app id such as "org.kde.konsole" or a
// window class such as "kitty") is a terminal emulator.
func isTerminal(app string) bool {
	a := strings.ToLower(strings.TrimSuffix(app, ".desktop"))
	if a == "" {
		return false
	}
	if terminalApps[a] {
		return true
	}
	if i := strings.LastIndexByte(a, '.'); i >= 0 {
		return terminalApps[a[i+1:]]
	}
	return false
}

// desktopEntry is the .desktop file that names Footman to the desktop: the
// portal requires one for the app id, and dialogs show its name.
func desktopEntry(binary string) string {
	return `[Desktop Entry]
Type=Application
Name=Footman
Comment=Voice dictation and assistant
Exec=` + desktopExecQuote(binary) + ` daemon
Icon=audio-input-microphone
Terminal=false
NoDisplay=true
`
}

// desktopExecQuote quotes a path for a .desktop Exec line when needed.
func desktopExecQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", "$", `\\$`)
	return `"` + r.Replace(s) + `"`
}

// SetupInput installs the .desktop file for AppID, pointing at binary, and
// lists the files Input writes, for the install receipt.
func (in *Input) SetupInput(_ context.Context, binary string) (platform.InputFiles, error) {
	dir := filepath.Join(in.dataHome(), "applications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return platform.InputFiles{}, fmt.Errorf("linux input: %w", err)
	}
	path := filepath.Join(dir, in.appID+".desktop")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(desktopEntry(binary)), 0o644); err != nil {
		return platform.InputFiles{}, fmt.Errorf("linux input: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return platform.InputFiles{}, fmt.Errorf("linux input: %w", err)
	}
	return platform.InputFiles{Program: []string{path}, State: []string{in.tokenFile}}, nil
}
