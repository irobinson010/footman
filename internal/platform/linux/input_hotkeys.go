//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// Hotkeys use the XDG GlobalShortcuts portal, which reports press and
// release. On desktops whose portal lacks it, KDE's KGlobalAccel D-Bus
// service is used directly.

const (
	kglobalaccelDest  = "org.kde.kglobalaccel"
	kglobalaccelPath  = dbus.ObjectPath("/kglobalaccel")
	kglobalaccelIface = "org.kde.KGlobalAccel"
	kglobalaccelComp  = "org.kde.kglobalaccel.Component"

	// setShortcutKeys flags.
	kgaSetPresent    = 2
	kgaNoAutoloading = 4

	// hotkeyBindTimeout bounds waiting for the desktop's shortcut dialog.
	hotkeyBindTimeout = 2 * time.Minute
)

// gsShortcut is one shortcut in BindShortcuts, a(sa{sv}).
type gsShortcut struct {
	ID   string
	Opts map[string]dbus.Variant
}

// kgaKeys is a QKeySequence on the wire: four key codes.
type kgaKeys struct{ Keys []int32 }

func parseHotkeys(combos []string) ([]hotkey, error) {
	out := make([]hotkey, 0, len(combos))
	seen := map[string]bool{}
	for _, c := range combos {
		h, err := parseHotkey(c)
		if err != nil {
			return nil, fmt.Errorf("linux input: %w", err)
		}
		if seen[h.id()] {
			continue
		}
		seen[h.id()] = true
		out = append(out, h)
	}
	return out, nil
}

// Hotkeys registers the combinations and reports their presses and releases
// until ctx is done. The desktop may have the shortcut on a different key
// than asked (the user can change it in the desktop's settings); events still
// carry the combination as given here.
//
// On KDE it registers with KGlobalAccel directly, keeping the key stored for
// each shortcut: KDE's GlobalShortcuts portal shows its "Global Shortcuts
// Requested" dialog for every new session, even when every shortcut is
// already bound, which would mean a dialog at each login (ADR-0030). Setup
// binds through the portal (BindHotkeys), so the shortcuts are already
// stored under Footman's app id. Elsewhere the portal is used.
func (in *Input) Hotkeys(ctx context.Context, combos []string) (<-chan platform.HotkeyEvent, error) {
	hs, err := parseHotkeys(combos)
	if err != nil {
		return nil, err
	}
	if len(hs) == 0 {
		return nil, errors.New("linux input: no hotkeys given")
	}
	p, err := in.portal()
	if err != nil {
		return nil, fmt.Errorf("linux input: %w: %w", platform.ErrUnsupported, err)
	}
	if in.forcePortal {
		return in.portalHotkeys(ctx, p, hs)
	}
	if _, _, kerr := in.kglobalaccel(ctx); kerr == nil || in.forceKGlobalAccel || p.version(ctx, portalGS) == 0 {
		return in.kglobalaccelHotkeys(ctx, hs)
	}
	return in.portalHotkeys(ctx, p, hs)
}

// bindPortal opens a GlobalShortcuts session and binds hs in it, returning
// the session and the desktop's description of each bound trigger by id.
func (in *Input) bindPortal(ctx context.Context, p *portalConn, hs []hotkey) (dbus.ObjectPath, map[string]string, error) {
	s, err := p.createSession(ctx, portalGS, "footman_hotkeys")
	if err != nil {
		return "", nil, err
	}
	shortcuts := make([]gsShortcut, 0, len(hs))
	for _, h := range hs {
		shortcuts = append(shortcuts, gsShortcut{h.id(), map[string]dbus.Variant{
			"description":       dbus.MakeVariant(h.description()),
			"preferred_trigger": dbus.MakeVariant(h.trigger()),
		}})
	}
	bctx, cancel := context.WithTimeout(ctx, hotkeyBindTimeout)
	defer cancel()
	res, err := p.request(bctx, portalGS+".BindShortcuts", s, shortcuts, "", map[string]dbus.Variant{})
	if err != nil {
		p.closeSession(s)
		return "", nil, err
	}
	return s, triggers(res), nil
}

// triggers reads trigger_description per shortcut id from a BindShortcuts or
// ListShortcuts response.
func triggers(res map[string]dbus.Variant) map[string]string {
	out := map[string]string{}
	v, ok := res["shortcuts"]
	if !ok {
		return out
	}
	var list []gsShortcut
	if err := dbus.Store([]any{v.Value()}, &list); err != nil {
		return out
	}
	for _, sc := range list {
		t := ""
		if d, ok := sc.Opts["trigger_description"]; ok {
			t, _ = d.Value().(string)
		}
		out[sc.ID] = t
	}
	return out
}

func (in *Input) portalHotkeys(ctx context.Context, p *portalConn, hs []hotkey) (<-chan platform.HotkeyEvent, error) {
	byID := map[string]string{}
	for _, h := range hs {
		byID[h.id()] = h.combo
	}
	sigs, cancelSigs := p.subscribe(func(s *dbus.Signal) bool {
		return s.Name == portalGS+".Activated" || s.Name == portalGS+".Deactivated"
	}, 64)
	s, _, err := in.bindPortal(ctx, p, hs)
	if err != nil {
		cancelSigs()
		return nil, fmt.Errorf("linux input: binding hotkeys: %w", err)
	}
	closed, cancelClosed := p.sessionClosed(s)
	out := make(chan platform.HotkeyEvent, 16)
	go func() {
		defer close(out)
		defer cancelSigs()
		defer cancelClosed()
		defer p.closeSession(s)
		for {
			select {
			case sig, ok := <-sigs:
				if !ok {
					return
				}
				// Body: session handle, shortcut id, timestamp, options. KDE
				// sends a zero timestamp, so our own clock is used.
				if len(sig.Body) < 2 {
					continue
				}
				if sess, _ := sig.Body[0].(dbus.ObjectPath); sess != s {
					continue
				}
				id, _ := sig.Body[1].(string)
				combo, ok := byID[id]
				if !ok {
					continue
				}
				ev := platform.HotkeyEvent{Hotkey: combo, Pressed: sig.Name == portalGS+".Activated", At: time.Now()}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-closed:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// kglobalaccel returns KDE's global shortcut service, or an error wrapping
// platform.ErrUnsupported if it is not running.
func (in *Input) kglobalaccel(ctx context.Context) (*dbus.Conn, dbus.BusObject, error) {
	p, err := in.portal()
	if err != nil {
		return nil, nil, err
	}
	c := p.conn
	var has bool
	if err := c.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, kglobalaccelDest).Store(&has); err != nil || !has {
		return nil, nil, fmt.Errorf("linux input: %w: no GlobalShortcuts portal and no KGlobalAccel", platform.ErrUnsupported)
	}
	return c, c.Object(kglobalaccelDest, kglobalaccelPath), nil
}

func (in *Input) kgaActionID(h hotkey) []string {
	return []string{in.appID, h.id(), "Footman", h.description()}
}

func (in *Input) kglobalaccelHotkeys(ctx context.Context, hs []hotkey) (<-chan platform.HotkeyEvent, error) {
	c, ka, err := in.kglobalaccel(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]string{}
	var registered [][]string
	unregister := func() {
		for _, id := range registered {
			_ = ka.Call(kglobalaccelIface+".setInactive", 0, id).Err
		}
	}
	for _, h := range hs {
		id := in.kgaActionID(h)
		// A key stored for the shortcut (bound by setup, or moved by the
		// user in System Settings) is kept. Only a shortcut with no key
		// gets the asked-for one, and only if no other app uses it.
		keys := in.kgaStoredKeys(ctx, ka, id)
		if keys == nil {
			if err := in.kgaCheckFree(ctx, ka, h); err != nil {
				unregister()
				return nil, err
			}
			keys = []kgaKeys{{[]int32{h.qtKey(), 0, 0, 0}}}
		}
		if err := ka.CallWithContext(ctx, kglobalaccelIface+".doRegister", 0, id).Err; err != nil {
			unregister()
			return nil, fmt.Errorf("linux input: KGlobalAccel: %w", err)
		}
		registered = append(registered, id)
		var got []kgaKeys
		if err := ka.CallWithContext(ctx, kglobalaccelIface+".setShortcutKeys", 0, id, keys, uint32(kgaSetPresent|kgaNoAutoloading)).Store(&got); err != nil {
			unregister()
			return nil, fmt.Errorf("linux input: KGlobalAccel: %w", err)
		}
		if len(got) == 0 || len(got[0].Keys) == 0 || got[0].Keys[0] == 0 {
			unregister()
			return nil, fmt.Errorf("linux input: KGlobalAccel did not assign %s (is it taken?)", h.combo)
		}
		byID[h.id()] = h.combo
	}
	var comp dbus.ObjectPath
	if err := ka.CallWithContext(ctx, kglobalaccelIface+".getComponent", 0, in.appID).Store(&comp); err != nil {
		unregister()
		return nil, fmt.Errorf("linux input: KGlobalAccel: %w", err)
	}
	if err := c.AddMatchSignal(dbus.WithMatchObjectPath(comp), dbus.WithMatchInterface(kglobalaccelComp)); err != nil {
		unregister()
		return nil, err
	}
	p, _ := in.portal()
	sigs, cancel := p.subscribe(func(s *dbus.Signal) bool {
		return s.Path == comp && (s.Name == kglobalaccelComp+".globalShortcutPressed" || s.Name == kglobalaccelComp+".globalShortcutReleased")
	}, 64)
	out := make(chan platform.HotkeyEvent, 16)
	go func() {
		defer close(out)
		defer cancel()
		defer unregister() // gives the keys back to other apps while we are not running
		for {
			select {
			case sig, ok := <-sigs:
				if !ok {
					return
				}
				// Body: component, action, timestamp.
				if len(sig.Body) < 2 {
					continue
				}
				action, _ := sig.Body[1].(string)
				combo, ok := byID[action]
				if !ok {
					continue
				}
				ev := platform.HotkeyEvent{Hotkey: combo, Pressed: strings.HasSuffix(sig.Name, "Pressed"), At: time.Now()}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// kgaStoredKeys returns the keys KGlobalAccel has stored for action id, or
// nil if it has none.
func (in *Input) kgaStoredKeys(ctx context.Context, ka dbus.BusObject, id []string) []kgaKeys {
	var keys []kgaKeys
	if err := ka.CallWithContext(ctx, kglobalaccelIface+".shortcutKeys", 0, id).Store(&keys); err != nil {
		return nil
	}
	if len(keys) == 0 || len(keys[0].Keys) == 0 || keys[0].Keys[0] == 0 {
		return nil
	}
	return keys
}

// kgaShortcutInfo is one entry of globalShortcutsByKey, a(ssssssaiai), in
// KGlobalShortcutInfo's wire order: the action, its component, its context
// (checked against KDE Plasma 6's kglobalaccel in the nested tests).
type kgaShortcutInfo struct {
	UniqueName, FriendlyName           string
	ComponentUnique, ComponentFriendly string
	ContextUnique, ContextFriendly     string
	Keys, DefaultKeys                  []int32
}

// kgaCheckFree returns an error naming the owner if another component
// already uses h's key, rather than silently taking it.
func (in *Input) kgaCheckFree(ctx context.Context, ka dbus.BusObject, h hotkey) error {
	var infos []kgaShortcutInfo
	err := ka.CallWithContext(ctx, kglobalaccelIface+".globalShortcutsByKey", 0,
		kgaKeys{[]int32{h.qtKey(), 0, 0, 0}}, struct{ T int32 }{0}).Store(&infos)
	if err != nil {
		return nil // an old KGlobalAccel without the query: let setShortcutKeys decide
	}
	for _, i := range infos {
		if i.ComponentUnique != in.appID {
			return fmt.Errorf("linux input: %s is already used by %q in %q; pick another key, or free it in System Settings > Keyboard > Shortcuts",
				h.combo, i.FriendlyName, i.ComponentFriendly)
		}
	}
	return nil
}

// BindHotkeys binds each combination to exactly that key, which is what
// setup calls after asking the user for their key, and again to rebind. The
// portal's suggested trigger is not relied on: KDE may store it silently or
// keep an older choice, and other desktops may ignore it. So, after binding,
// any shortcut not on the asked-for key is moved there through KGlobalAccel
// (KDE), or the desktop's own shortcut dialog is opened (other desktops with
// portal version 2). It returns what the desktop reports each combination is
// bound to.
func (in *Input) BindHotkeys(ctx context.Context, combos []string) ([]platform.HotkeyBinding, error) {
	hs, err := parseHotkeys(combos)
	if err != nil {
		return nil, err
	}
	p, err := in.portal()
	if err != nil {
		return nil, fmt.Errorf("linux input: %w: %w", platform.ErrUnsupported, err)
	}
	if in.forceKGlobalAccel || p.version(ctx, portalGS) == 0 {
		return in.bindKGlobalAccel(ctx, hs)
	}
	s, got, err := in.bindPortal(ctx, p, hs)
	if err != nil {
		return nil, fmt.Errorf("linux input: binding hotkeys: %w", err)
	}
	defer p.closeSession(s)

	_, ka, kerr := in.kglobalaccel(ctx)
	changed := false
	for _, h := range hs {
		if kerr != nil {
			continue
		}
		// KDE's portal may report the suggested key although it left the
		// shortcut without one (when another app holds the key), so what
		// KGlobalAccel stored is what counts.
		if stored := in.kgaStoredKeys(ctx, ka, in.kgaActionID(h)); stored != nil && stored[0].Keys[0] == h.qtKey() {
			got[h.id()] = h.combo
			continue
		}
		if err := in.kgaCheckFree(ctx, ka, h); err != nil {
			return nil, err
		}
		keys := []kgaKeys{{[]int32{h.qtKey(), 0, 0, 0}}}
		if err := ka.CallWithContext(ctx, kglobalaccelIface+".setForeignShortcutKeys", 0, in.kgaActionID(h), keys).Err; err != nil {
			return nil, fmt.Errorf("linux input: setting %s: %w", h.combo, err)
		}
		changed = true
	}
	if !changed && kerr != nil && p.version(ctx, portalGS) >= 2 {
		for _, h := range hs {
			if !sameTrigger(got[h.id()], h) {
				// Let the user choose in the desktop's dialog.
				_ = p.obj().CallWithContext(ctx, portalGS+".ConfigureShortcuts", 0, s, "", map[string]dbus.Variant{}).Err
				changed = true
				break
			}
		}
	}
	if changed {
		if res, err := p.request(ctx, portalGS+".ListShortcuts", s, map[string]dbus.Variant{}); err == nil {
			got = triggers(res)
		}
	}
	out := make([]platform.HotkeyBinding, 0, len(hs))
	for _, h := range hs {
		t := got[h.id()]
		if sameTrigger(t, h) {
			t = h.combo // "Meta+Shift+Space" for "Super+Shift+Space"
		}
		out = append(out, platform.HotkeyBinding{Hotkey: h.combo, Trigger: t})
	}
	return out, nil
}

func (in *Input) bindKGlobalAccel(ctx context.Context, hs []hotkey) ([]platform.HotkeyBinding, error) {
	_, ka, err := in.kglobalaccel(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]platform.HotkeyBinding, 0, len(hs))
	for _, h := range hs {
		if err := in.kgaCheckFree(ctx, ka, h); err != nil {
			return nil, err
		}
		id := in.kgaActionID(h)
		if err := ka.CallWithContext(ctx, kglobalaccelIface+".doRegister", 0, id).Err; err != nil {
			return nil, fmt.Errorf("linux input: KGlobalAccel: %w", err)
		}
		var got []kgaKeys
		keys := []kgaKeys{{[]int32{h.qtKey(), 0, 0, 0}}}
		if err := ka.CallWithContext(ctx, kglobalaccelIface+".setShortcutKeys", 0, id, keys, uint32(kgaSetPresent|kgaNoAutoloading)).Store(&got); err != nil {
			return nil, fmt.Errorf("linux input: KGlobalAccel: %w", err)
		}
		_ = ka.Call(kglobalaccelIface+".setInactive", 0, id).Err
		t := ""
		if len(got) > 0 && len(got[0].Keys) > 0 && got[0].Keys[0] != 0 {
			t = h.combo
		}
		out = append(out, platform.HotkeyBinding{Hotkey: h.combo, Trigger: t})
	}
	return out, nil
}

// sameTrigger reports whether the desktop's description of a binding (such
// as "Ctrl+Shift+Space" or "F8") names h's key.
func sameTrigger(desc string, h hotkey) bool {
	if desc == "" {
		return false
	}
	// A shortcut can have several keys, listed with ", " or tabs.
	for _, alt := range strings.FieldsFunc(desc, func(r rune) bool { return r == ',' || r == '\t' }) {
		d, err := parseHotkey(strings.TrimSpace(alt))
		if err == nil && d.id() == h.id() {
			return true
		}
	}
	return false
}
