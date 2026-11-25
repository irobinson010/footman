//go:build linux

package linux

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// ---- notifications ----

type fakeNotifd struct {
	mu   sync.Mutex
	got  []string // summary, body
	acts []string
	hint map[string]dbus.Variant
}

func (f *fakeNotifd) Notify(app string, replaces uint32, icon, summary, body string, actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, summary, body)
	f.acts = actions
	f.hint = hints
	return 7, nil
}

func TestNotifier(t *testing.T) {
	addr := startBus(t)
	srv := serve(t, addr, "org.freedesktop.Notifications")
	f := &fakeNotifd{}
	if err := srv.Export(f, "/org/freedesktop/Notifications", "org.freedesktop.Notifications"); err != nil {
		t.Fatal(err)
	}
	p := newTestPlatform(t, addr, defaultRun)
	n := p.Notifier()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := n.Notify(ctx, platform.Notification{Title: "Run it?", Body: "rm -rf /tmp/x",
		Actions: []platform.Action{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.got[0] != "Run it?" || len(f.acts) != 4 || f.acts[0] != "yes" || f.acts[3] != "No" {
		t.Errorf("server saw %v %v", f.got, f.acts)
	}
	f.mu.Unlock()

	if err := srv.Emit("/org/freedesktop/Notifications", "org.freedesktop.Notifications.ActionInvoked", uint32(7), "yes"); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-ch:
		if a != "yes" {
			t.Errorf("action = %q", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no action received")
	}
	// Clicking closes the notification, which closes the channel.
	if err := srv.Emit("/org/freedesktop/Notifications", "org.freedesktop.Notifications.NotificationClosed", uint32(7), uint32(2)); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("unexpected extra value")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed")
	}

	// ctx cancel closes the channel.
	ctx2, cancel2 := context.WithCancel(context.Background())
	ch2, err := n.Notify(ctx2, platform.Notification{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	cancel2()
	select {
	case _, ok := <-ch2:
		if ok {
			t.Error("unexpected value")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed on cancel")
	}
	if err := n.SetTray(ctx, platform.Recording); err != nil {
		t.Error(err)
	}
}

func TestNotifierNoServer(t *testing.T) {
	addr := startBus(t)
	p := newTestPlatform(t, addr, defaultRun)
	_, err := p.Notifier().Notify(context.Background(), platform.Notification{Title: "x"})
	if !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

// ---- secret service ----

type fakeSecrets struct {
	mu     sync.Mutex
	items  map[string]string // key -> value
	locked bool
	conn   *dbus.Conn
}

type fsService struct{ f *fakeSecrets }
type fsColl struct{ f *fakeSecrets }
type fsItemObj struct {
	f   *fakeSecrets
	key string
}
type fsPromptObj struct{ f *fakeSecrets }

func itemPath(key string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/secrets/item/k" + hexs(key))
}
func hexs(s string) string {
	const h = "0123456789abcdef"
	var out []byte
	for _, b := range []byte(s) {
		out = append(out, h[b>>4], h[b&15])
	}
	return string(out)
}

func (s fsService) OpenSession(alg string, in dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	if alg != "plain" {
		return dbus.Variant{}, "/", dbus.MakeFailedError(errors.New("only plain"))
	}
	return dbus.MakeVariant(""), "/org/freedesktop/secrets/session/s1", nil
}

func (s fsService) SearchItems(attrs map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, *dbus.Error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if attrs["application"] != "footman" {
		return nil, nil, nil
	}
	if _, ok := s.f.items[attrs["key"]]; !ok {
		return []dbus.ObjectPath{}, []dbus.ObjectPath{}, nil
	}
	p := itemPath(attrs["key"])
	if s.f.locked {
		return []dbus.ObjectPath{}, []dbus.ObjectPath{p}, nil
	}
	return []dbus.ObjectPath{p}, []dbus.ObjectPath{}, nil
}

func (s fsService) Unlock(objs []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.f.mu.Lock()
		s.f.locked = false
		s.f.mu.Unlock()
		_ = s.f.conn.Emit("/org/freedesktop/secrets/prompt/p1", "org.freedesktop.Secret.Prompt.Completed", false, dbus.MakeVariant(objs))
	}()
	return []dbus.ObjectPath{}, "/org/freedesktop/secrets/prompt/p1", nil
}

func (p fsPromptObj) Prompt(window string) *dbus.Error { return nil }
func (p fsPromptObj) Dismiss() *dbus.Error             { return nil }

func (c fsColl) CreateItem(props map[string]dbus.Variant, sec secret, replace bool) (dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	attrs, _ := props["org.freedesktop.Secret.Item.Attributes"].Value().(map[string]string)
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.items[attrs["key"]] = string(sec.Value)
	return itemPath(attrs["key"]), "/", nil
}

func (i fsItemObj) GetSecret(session dbus.ObjectPath) (secret, *dbus.Error) {
	i.f.mu.Lock()
	defer i.f.mu.Unlock()
	return secret{Session: session, Parameters: []byte{}, Value: []byte(i.f.items[i.key]), ContentType: "text/plain"}, nil
}

func (i fsItemObj) Delete() (dbus.ObjectPath, *dbus.Error) {
	i.f.mu.Lock()
	defer i.f.mu.Unlock()
	delete(i.f.items, i.key)
	return "/", nil
}

func newFakeSecrets(t *testing.T, addr string, keys ...string) *fakeSecrets {
	srv := serve(t, addr, "org.freedesktop.secrets")
	f := &fakeSecrets{items: map[string]string{}, conn: srv}
	exp := func(v any, path dbus.ObjectPath, iface string) {
		if err := srv.Export(v, path, iface); err != nil {
			t.Fatal(err)
		}
	}
	exp(fsService{f}, "/org/freedesktop/secrets", "org.freedesktop.Secret.Service")
	exp(fsColl{f}, "/org/freedesktop/secrets/aliases/default", "org.freedesktop.Secret.Collection")
	exp(fsPromptObj{f}, "/org/freedesktop/secrets/prompt/p1", "org.freedesktop.Secret.Prompt")
	for _, k := range keys {
		exp(fsItemObj{f, k}, itemPath(k), "org.freedesktop.Secret.Item")
	}
	return f
}

func TestSecrets(t *testing.T) {
	addr := startBus(t)
	f := newFakeSecrets(t, addr, "claude")
	s := newTestPlatform(t, addr, defaultRun).Secrets()
	ctx := context.Background()

	if _, err := s.Get(ctx, "claude"); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
	if err := s.Set(ctx, "claude", "sk-123"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, "claude"); err != nil || got != "sk-123" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Set(ctx, "claude", "sk-456"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "claude"); got != "sk-456" {
		t.Errorf("replace: got %q", got)
	}

	// A locked keyring is unlocked through its prompt.
	f.mu.Lock()
	f.locked = true
	f.mu.Unlock()
	if got, err := s.Get(ctx, "claude"); err != nil || got != "sk-456" {
		t.Errorf("locked Get = %q, %v", got, err)
	}

	if err := s.Delete(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "claude"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if err := s.Delete(ctx, "claude"); err != nil {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestSecretsNoKeyring(t *testing.T) {
	addr := startBus(t)
	s := newTestPlatform(t, addr, defaultRun).Secrets()
	err := s.Set(context.Background(), "k", "v")
	if !errors.Is(err, ErrNoKeyring) || !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
}

// ---- KWin ----

type fakeKWin struct {
	conn    *dbus.Conn
	windows [][5]string // id, caption, class, pid
	active  int
	focused string
	script  string
	mu      sync.Mutex
}

var (
	reDest  = regexp.MustCompile(`callDBus\("([^"]+)"`)
	reToken = regexp.MustCompile(`"Report",\s*"([0-9a-f]+)"`)
)

type kwScripting struct{ f *fakeKWin }
type kwScript struct{ f *fakeKWin }

func (s kwScripting) LoadScript(path, name string) (int32, *dbus.Error) {
	b, err := readFile(path)
	if err != nil {
		return 0, dbus.MakeFailedError(err)
	}
	s.f.mu.Lock()
	s.f.script = b
	s.f.mu.Unlock()
	return 3, nil
}
func (s kwScripting) UnloadScript(name string) (bool, *dbus.Error) { return true, nil }

// Run plays the script's answer. It does not interpret the JavaScript; it
// recognises which of the three scripts this is by its text.
func (s kwScript) Run() *dbus.Error {
	f := s.f
	f.mu.Lock()
	script := f.script
	f.mu.Unlock()
	dest := reDest.FindStringSubmatch(script)[1]
	token := reToken.FindStringSubmatch(script)[1]
	type win struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		App   string `json:"app"`
		PID   string `json:"pid"`
	}
	conv := func(w [5]string) win { return win{w[0], w[1], w[2], w[3]} }
	op := "ok"
	out := []win{}
	f.mu.Lock()
	switch {
	case contains(script, "workspace.activeWindow = all[i]"):
		op = "fail"
		for _, w := range f.windows {
			if contains(script, `"`+w[0]+`"`) {
				f.focused, op = w[0], "ok"
			}
		}
	case contains(script, "windowList()"):
		for _, w := range f.windows {
			out = append(out, conv(w))
		}
	default:
		if f.active >= 0 {
			out = append(out, conv(f.windows[f.active]))
		}
	}
	f.mu.Unlock()
	b, _ := json.Marshal(out)
	_ = f.conn.Object(dest, recvPath).Call(recvIface+".Report", dbus.FlagNoReplyExpected, token, op, string(b)).Err
	return nil
}

func TestWindows(t *testing.T) {
	addr := startBus(t)
	srv := serve(t, addr, "org.kde.KWin")
	f := &fakeKWin{conn: srv, active: 1, windows: [][5]string{
		{"{aaa}", "~ : bash — Konsole", "org.kde.konsole", "100", ""},
		{"{bbb}", "Docs — Mozilla Firefox", "firefox", strconv.Itoa(200), ""},
	}}
	if err := srv.ExportWithMap(kwScripting{f}, map[string]string{"LoadScript": "loadScript", "UnloadScript": "unloadScript"}, "/Scripting", "org.kde.kwin.Scripting"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ExportWithMap(kwScript{f}, map[string]string{"Run": "run"}, "/Scripting/Script3", "org.kde.kwin.Script"); err != nil {
		t.Fatal(err)
	}
	w := newTestPlatform(t, addr, defaultRun).Windows()
	ctx := context.Background()

	a, err := w.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "{bbb}" || a.App != "firefox" || a.PID != 200 || a.Title != "Docs — Mozilla Firefox" {
		t.Errorf("Active = %+v", a)
	}
	l, err := w.List(ctx)
	if err != nil || len(l) != 2 || l[0].App != "org.kde.konsole" || l[0].PID != 100 {
		t.Errorf("List = %+v, %v", l, err)
	}
	err = w.Focus(ctx, "{aaa}")
	f.mu.Lock()
	focused := f.focused
	f.mu.Unlock()
	if err != nil || focused != "{aaa}" {
		t.Errorf("Focus = %v, focused %q", err, focused)
	}
	if err := w.Focus(ctx, "{zzz}"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("Focus unknown = %v", err)
	}
	f.mu.Lock()
	f.active = -1
	f.mu.Unlock()
	if _, err := w.Active(ctx); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("no active window = %v", err)
	}
}

func TestWindowsNoKWin(t *testing.T) {
	addr := startBus(t)
	w := newTestPlatform(t, addr, defaultRun).Windows()
	if _, err := w.Active(context.Background()); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
}
