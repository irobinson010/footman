//go:build linux

package linux

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakePortal serves the parts of xdg-desktop-portal Input uses on a private
// test bus: the host Registry, RemoteDesktop and GlobalShortcuts, plus KDE's
// kglobalaccel when withKGA is set.
type fakePortal struct {
	t    *testing.T
	conn *dbus.Conn

	mu         sync.Mutex
	registered []string
	// refuseRegister is how many Register calls to refuse (-1: all).
	refuseRegister int
	gsVersion      uint32
	// RemoteDesktop.
	starts     int
	selectOpts []map[string]dbus.Variant
	nextToken  int
	denyStart  bool
	keys       []keyEvent
	onKey      func(keyEvent)
	sessions   []dbus.ObjectPath
	// GlobalShortcuts: trigger per shortcut id as the desktop stores it.
	gsTriggers map[string]string
	gsSession  dbus.ObjectPath
	bound      [][]gsShortcut
	configured int
	// kglobalaccel.
	foreign   map[string][]int32 // action -> keys set with setForeignShortcutKeys
	kgaKeys   map[string]int32   // action -> key set with setShortcutKeys
	kgaTaken  map[int32]string   // key -> other component's action
	inactive  []string
	kgaActive bool
	kgaConn   *dbus.Conn
}

type keyEvent struct {
	sym     uint32
	pressed bool
}

const (
	fakePortalPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	fakeKGAComp    = dbus.ObjectPath("/component/footman")
)

func newFakePortal(t *testing.T, addr string, withKGA bool) *fakePortal {
	t.Helper()
	f := &fakePortal{t: t, conn: serve(t, addr, portalDest), gsVersion: 2,
		gsTriggers: map[string]string{}, foreign: map[string][]int32{}, kgaKeys: map[string]int32{}, kgaTaken: map[int32]string{}}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.conn.ExportMethodTable(map[string]any{
		"Register": func(appID string, _ map[string]dbus.Variant) *dbus.Error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.refuseRegister != 0 {
				// As xdg-desktop-portal before it has seen a new .desktop file.
				f.refuseRegister--
				return dbus.NewError("org.freedesktop.portal.Error.Failed", []any{"Could not register app ID: App info not found for '" + appID + "'"})
			}
			f.registered = append(f.registered, appID)
			return nil
		},
	}, fakePortalPath, portalRegistry))
	must(f.conn.ExportMethodTable(map[string]any{
		"Get": func(iface, prop string) (dbus.Variant, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if iface == portalGS && prop == "version" && f.gsVersion > 0 {
				return dbus.MakeVariant(f.gsVersion), nil
			}
			if iface == portalRD && prop == "version" {
				return dbus.MakeVariant(uint32(2)), nil
			}
			return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
		},
	}, fakePortalPath, "org.freedesktop.DBus.Properties"))
	must(f.conn.ExportMethodTable(map[string]any{
		"CreateSession": func(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			return f.createSession(sender, opts)
		},
		"SelectDevices": func(sender dbus.Sender, s dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			f.mu.Lock()
			f.selectOpts = append(f.selectOpts, opts)
			f.mu.Unlock()
			return f.respond(sender, opts, 0, nil), nil
		},
		"Start": func(sender dbus.Sender, s dbus.ObjectPath, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			f.mu.Lock()
			f.starts++
			f.nextToken++
			deny := f.denyStart
			tok := fmt.Sprintf("token-%d", f.nextToken)
			f.mu.Unlock()
			if deny {
				return f.respond(sender, opts, 1, nil), nil
			}
			return f.respond(sender, opts, 0, map[string]dbus.Variant{
				"devices":       dbus.MakeVariant(uint32(1)),
				"restore_token": dbus.MakeVariant(tok),
			}), nil
		},
		"NotifyKeyboardKeysym": func(s dbus.ObjectPath, _ map[string]dbus.Variant, sym int32, state uint32) *dbus.Error {
			ev := keyEvent{uint32(sym), state == 1}
			f.mu.Lock()
			f.keys = append(f.keys, ev)
			on := f.onKey
			f.mu.Unlock()
			if on != nil {
				on(ev)
			}
			return nil
		},
	}, fakePortalPath, portalRD))
	must(f.conn.ExportMethodTable(map[string]any{
		"CreateSession": func(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			p, err := f.createSession(sender, opts)
			f.mu.Lock()
			f.gsSession = f.sessions[len(f.sessions)-1]
			f.mu.Unlock()
			return p, err
		},
		"BindShortcuts": func(sender dbus.Sender, s dbus.ObjectPath, shortcuts []gsShortcut, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			f.mu.Lock()
			f.bound = append(f.bound, shortcuts)
			for _, sc := range shortcuts {
				if _, ok := f.gsTriggers[sc.ID]; !ok {
					// Like KDE: a new shortcut gets the preferred trigger.
					pt, _ := sc.Opts["preferred_trigger"].Value().(string)
					f.gsTriggers[sc.ID] = xdgToKDE(pt)
				}
			}
			res := f.shortcutsLocked(shortcuts)
			f.mu.Unlock()
			return f.respond(sender, opts, 0, res), nil
		},
		"ListShortcuts": func(sender dbus.Sender, s dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
			f.mu.Lock()
			var all []gsShortcut
			if len(f.bound) > 0 {
				all = f.bound[len(f.bound)-1]
			}
			res := f.shortcutsLocked(all)
			f.mu.Unlock()
			return f.respond(sender, opts, 0, res), nil
		},
		"ConfigureShortcuts": func(s dbus.ObjectPath, parent string, opts map[string]dbus.Variant) *dbus.Error {
			f.mu.Lock()
			f.configured++
			f.mu.Unlock()
			return nil
		},
	}, fakePortalPath, portalGS))
	if withKGA {
		f.exportKGA(t, addr)
	}
	return f
}

// xdgToKDE turns "CTRL+SHIFT+space" into KDE's "Ctrl+Shift+Space".
func xdgToKDE(s string) string {
	parts := strings.Split(s, "+")
	for i, p := range parts {
		switch p {
		case "CTRL":
			parts[i] = "Ctrl"
		case "SHIFT":
			parts[i] = "Shift"
		case "ALT":
			parts[i] = "Alt"
		case "LOGO":
			parts[i] = "Meta"
		default:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

func (f *fakePortal) shortcutsLocked(list []gsShortcut) map[string]dbus.Variant {
	out := make([]gsShortcut, 0, len(list))
	for _, sc := range list {
		out = append(out, gsShortcut{sc.ID, map[string]dbus.Variant{
			"description":         dbus.MakeVariant("x"),
			"trigger_description": dbus.MakeVariant(f.gsTriggers[sc.ID]),
		}})
	}
	return map[string]dbus.Variant{"shortcuts": dbus.MakeVariant(out)}
}

func escapeSender(s dbus.Sender) string {
	return strings.ReplaceAll(strings.TrimPrefix(string(s), ":"), ".", "_")
}

func (f *fakePortal) createSession(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	tok, _ := opts["session_handle_token"].Value().(string)
	sp := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/" + escapeSender(sender) + "/" + tok)
	f.mu.Lock()
	f.sessions = append(f.sessions, sp)
	f.mu.Unlock()
	_ = f.conn.ExportMethodTable(map[string]any{"Close": func() *dbus.Error { return nil }}, sp, portalSession)
	return f.respond(sender, opts, 0, map[string]dbus.Variant{"session_handle": dbus.MakeVariant(string(sp))}), nil
}

// respond returns the request handle and emits its Response soon after.
func (f *fakePortal) respond(sender dbus.Sender, opts map[string]dbus.Variant, code uint32, res map[string]dbus.Variant) dbus.ObjectPath {
	tok, _ := opts["handle_token"].Value().(string)
	h := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + escapeSender(sender) + "/" + tok)
	if res == nil {
		res = map[string]dbus.Variant{}
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = f.conn.Emit(h, portalRequest+".Response", code, res)
	}()
	return h
}

// closeSession emits Session.Closed, as when the user stops it from the tray.
func (f *fakePortal) closeSession(s dbus.ObjectPath) {
	_ = f.conn.Emit(s, portalSession+".Closed", map[string]dbus.Variant{})
}

// shortcut emits Activated or Deactivated for id.
func (f *fakePortal) shortcut(id string, pressed bool) {
	f.mu.Lock()
	s := f.gsSession
	f.mu.Unlock()
	name := portalGS + ".Deactivated"
	if pressed {
		name = portalGS + ".Activated"
	}
	_ = f.conn.Emit(fakePortalPath, name, s, id, uint64(0), map[string]dbus.Variant{})
}

// set changes the fake's state under its lock.
func (f *fakePortal) set(fn func(*fakePortal)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// get reads the fake's state under its lock.
func (f *fakePortal) get(fn func(*fakePortal) any) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fn(f)
}

func (f *fakePortal) keyLog() []keyEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]keyEvent(nil), f.keys...)
}

func (f *fakePortal) exportKGA(t *testing.T, addr string) {
	c := serve(t, addr, kglobalaccelDest)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.ExportMethodTable(map[string]any{
		"doRegister": func(id []string) *dbus.Error {
			f.mu.Lock()
			f.kgaActive = true
			f.mu.Unlock()
			return nil
		},
		"setShortcutKeys": func(id []string, keys []kgaKeys, flags uint32) ([]kgaKeys, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, taken := f.kgaTaken[keys[0].Keys[0]]; taken {
				return []kgaKeys{{[]int32{0, 0, 0, 0}}}, nil
			}
			f.kgaKeys[id[1]] = keys[0].Keys[0]
			return keys, nil
		},
		// The stored key: one set directly, or (as KDE stores what the
		// portal bound) the portal's trigger.
		"shortcutKeys": func(id []string) ([]kgaKeys, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if k, ok := f.foreign[id[1]]; ok {
				return []kgaKeys{{k}}, nil
			}
			if k, ok := f.kgaKeys[id[1]]; ok {
				return []kgaKeys{{[]int32{k, 0, 0, 0}}}, nil
			}
			if h, err := parseHotkey(f.gsTriggers[id[1]]); err == nil {
				return []kgaKeys{{[]int32{h.qtKey(), 0, 0, 0}}}, nil
			}
			return []kgaKeys{}, nil
		},
		"setForeignShortcutKeys": func(id []string, keys []kgaKeys) *dbus.Error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.foreign[id[1]] = keys[0].Keys
			// The portal now reports the new key.
			for k, v := range map[int32]string{0x01000037: "F8", 0x01000038: "F9", 0x01000039: "F10"} {
				if keys[0].Keys[0] == k {
					f.gsTriggers[id[1]] = v
				}
			}
			return nil
		},
		"globalShortcutsByKey": func(key kgaKeys, match struct{ T int32 }) ([]kgaShortcutInfo, *dbus.Error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if who, ok := f.kgaTaken[key.Keys[0]]; ok {
				return []kgaShortcutInfo{{ComponentUnique: "kwin", ComponentFriendly: "KWin", UniqueName: who, FriendlyName: who, Keys: []int32{}, DefaultKeys: []int32{}}}, nil
			}
			return []kgaShortcutInfo{}, nil
		},
		"getComponent": func(name string) (dbus.ObjectPath, *dbus.Error) { return fakeKGAComp, nil },
		"setInactive": func(id []string) *dbus.Error {
			f.mu.Lock()
			f.inactive = append(f.inactive, id[1])
			f.mu.Unlock()
			return nil
		},
	}, kglobalaccelPath, kglobalaccelIface))
	f.kgaConn = c
}
