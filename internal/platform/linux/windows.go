//go:build linux

package linux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

const (
	kwinDest      = "org.kde.KWin"
	kwinScripting = "/Scripting"
	recvPath      = dbus.ObjectPath("/org/footman/Windows")
	recvIface     = "org.footman.Windows"
	windowTimeout = 5 * time.Second
)

// Windows reads and focuses windows through KWin's scripting D-Bus API. KWin
// has no "list windows" method, so a short script is loaded that reports the
// windows back by calling a method this process exports on the session bus.
// Only KDE Plasma is supported; elsewhere every method returns
// platform.ErrUnsupported.
type Windows struct {
	bus        *bus
	runtimeDir func() string

	mu       sync.Mutex
	exported *dbus.Conn
	pending  map[string]chan report
}

// report is one answer from the script: op "ok" with a JSON array of windows,
// or "fail".
type report struct {
	op      string
	payload string
}

// Report is called by the KWin script. It is exported on D-Bus, so it must be
// an exported method with D-Bus friendly arguments. The script answers with a
// single call carrying all windows as JSON, because KWin's callDBus calls are
// asynchronous and may arrive in any order.
func (w *Windows) Report(token, op, payload string) *dbus.Error {
	w.mu.Lock()
	ch := w.pending[token]
	w.mu.Unlock()
	if ch != nil {
		select {
		case ch <- report{op: op, payload: payload}:
		default:
		}
	}
	return nil
}

// Active returns the focused window.
func (w *Windows) Active(ctx context.Context) (platform.Window, error) {
	res, err := w.script(ctx, `
var a = workspace.activeWindow;
if (a) { out.push(info(a)); }
`)
	if err != nil {
		return platform.Window{}, err
	}
	if len(res) == 0 {
		return platform.Window{}, fmt.Errorf("linux windows: no active window: %w", platform.ErrNotFound)
	}
	return res[0], nil
}

// List returns the open top-level windows, skipping desktop, panel and other
// special windows.
func (w *Windows) List(ctx context.Context) ([]platform.Window, error) {
	return w.script(ctx, `
var all = workspace.windowList();
for (var i = 0; i < all.length; i++) {
  var x = all[i];
  if (x.normalWindow) { out.push(info(x)); }
}
`)
}

// Focus raises and focuses the window with the given ID.
func (w *Windows) Focus(ctx context.Context, id string) error {
	q, _ := json.Marshal(id)
	_, err := w.script(ctx, `
var want = `+string(q)+`;
var all = workspace.windowList();
var found = false;
for (var i = 0; i < all.length; i++) {
  if (String(all[i].internalId) === want) { workspace.activeWindow = all[i]; found = true; }
}
if (!found) { fail = true; }
`)
	return err
}

// script runs body in KWin and returns the windows it reported.
func (w *Windows) script(ctx context.Context, body string) ([]platform.Window, error) {
	conn, err := w.bus.get()
	if err != nil {
		return nil, fmt.Errorf("linux windows: %w: %w", platform.ErrUnsupported, err)
	}
	var has bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, kwinDest).Store(&has); err != nil || !has {
		return nil, fmt.Errorf("linux windows: %w: KWin (KDE Plasma) is not running", platform.ErrUnsupported)
	}
	if err := w.export(conn); err != nil {
		return nil, err
	}
	var rb [8]byte
	_, _ = rand.Read(rb[:])
	token := hex.EncodeToString(rb[:])
	ch := make(chan report, 256)
	w.mu.Lock()
	w.pending[token] = ch
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.pending, token)
		w.mu.Unlock()
	}()

	src := w.source(conn.Names()[0], token, body)
	dir := w.runtimeDir()
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.CreateTemp(dir, "footman-kwin-*.js")
	if err != nil {
		f, err = os.CreateTemp("", "footman-kwin-*.js")
		if err != nil {
			return nil, fmt.Errorf("linux windows: %w", err)
		}
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(src); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("linux windows: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("linux windows: %w", err)
	}

	name := "footman-" + token
	scripting := conn.Object(kwinDest, kwinScripting)
	var id int32
	if err := scripting.CallWithContext(ctx, "org.kde.kwin.Scripting.loadScript", 0, f.Name(), name).Store(&id); err != nil {
		return nil, fmt.Errorf("linux windows: loading KWin script: %w", err)
	}
	defer func() { _ = scripting.Call("org.kde.kwin.Scripting.unloadScript", 0, name).Err }()
	runPath := dbus.ObjectPath("/Scripting/Script" + strconv.Itoa(int(id)))
	if err := conn.Object(kwinDest, runPath).CallWithContext(ctx, "org.kde.kwin.Script.run", 0).Err; err != nil {
		return nil, fmt.Errorf("linux windows: running KWin script: %w", err)
	}

	select {
	case r := <-ch:
		if r.op == "fail" {
			return nil, fmt.Errorf("linux windows: window: %w", platform.ErrNotFound)
		}
		var raw []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			App   string `json:"app"`
			PID   string `json:"pid"`
		}
		if err := json.Unmarshal([]byte(r.payload), &raw); err != nil {
			return nil, fmt.Errorf("linux windows: bad answer from KWin: %w", err)
		}
		out := make([]platform.Window, 0, len(raw))
		for _, x := range raw {
			pid, _ := strconv.Atoi(x.PID)
			out = append(out, platform.Window{ID: x.ID, Title: x.Title, App: x.App, PID: pid})
		}
		return out, nil
	case <-time.After(windowTimeout):
		return nil, errors.New("linux windows: KWin did not answer in time")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *Windows) export(conn *dbus.Conn) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending == nil {
		w.pending = map[string]chan report{}
	}
	if w.exported == conn {
		return nil
	}
	if err := conn.Export(w, recvPath, recvIface); err != nil {
		return fmt.Errorf("linux windows: %w", err)
	}
	w.exported = conn
	return nil
}

// source wraps body in the helpers every script needs. body fills out and may
// set fail.
func (w *Windows) source(self, token, body string) string {
	return `// Generated by Footman. Reports windows back over D-Bus.
function info(w) {
  return { id: String(w.internalId), title: String(w.caption),
           app: String(w.desktopFileName || w.resourceClass), pid: String(w.pid) };
}
(function () {
var out = [];
var fail = false;
` + body + `
callDBus(` + strconv.Quote(self) + `, "` + string(recvPath) + `", "` + recvIface + `", "Report",
         "` + token + `", fail ? "fail" : "ok", JSON.stringify(out));
})();
`
}

var _ platform.Windows = (*Windows)(nil)
