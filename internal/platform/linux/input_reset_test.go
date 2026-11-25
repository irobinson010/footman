//go:build linux

package linux

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

type resetFake struct {
	mu       sync.Mutex
	perms    map[string][]string // table/id -> apps
	cleaned  bool
	noClean  bool
	unreg    []string
	hasStore bool
}

func (f *resetFake) export(t *testing.T, addr string) {
	if f.hasStore {
		c := serve(t, addr, permStoreDest)
		if err := c.ExportMethodTable(map[string]any{
			"List": func(table string) ([]string, *dbus.Error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				var ids []string
				for k := range f.perms {
					if tb, id, _ := cut(k); tb == table {
						ids = append(ids, id)
					}
				}
				return ids, nil
			},
			"DeletePermission": func(table, id, app string) *dbus.Error {
				f.mu.Lock()
				defer f.mu.Unlock()
				apps := f.perms[table+"/"+id]
				if !slices.Contains(apps, app) {
					return dbus.NewError("org.freedesktop.portal.Error.NotFound", nil)
				}
				f.perms[table+"/"+id] = slices.DeleteFunc(apps, func(a string) bool { return a == app })
				return nil
			},
		}, permStorePath, permStoreIface); err != nil {
			t.Fatal(err)
		}
	}
	c := serve(t, addr, kglobalaccelDest)
	if err := c.ExportMethodTable(map[string]any{
		"getComponent": func(name string) (dbus.ObjectPath, *dbus.Error) { return fakeKGAComp, nil },
		"unregister": func(comp, name string) *dbus.Error {
			f.mu.Lock()
			f.unreg = append(f.unreg, name)
			f.mu.Unlock()
			return nil
		},
	}, kglobalaccelPath, kglobalaccelIface); err != nil {
		t.Fatal(err)
	}
	comp := map[string]any{
		"shortcutNames": func() ([]string, *dbus.Error) { return []string{"hotkey-f8", "hotkey-shift-f8"}, nil },
	}
	if !f.noClean {
		comp["cleanUp"] = func() *dbus.Error {
			f.mu.Lock()
			f.cleaned = true
			f.mu.Unlock()
			return nil
		}
	}
	if err := c.ExportMethodTable(comp, fakeKGAComp, kglobalaccelComp); err != nil {
		t.Fatal(err)
	}
}

func cut(k string) (string, string, bool) {
	for i := range k {
		if k[i] == '/' {
			return k[:i], k[i+1:], true
		}
	}
	return k, "", false
}

func TestResetInput(t *testing.T) {
	ctx := context.Background()
	f := &resetFake{hasStore: true, perms: map[string][]string{
		"kde-authorized/remote-desktop": {AppID, "org.other.App"},
		"kde-authorized/other":          {"org.other.App"},
	}}
	addr := startBus(t)
	f.export(t, addr)
	p := newTestPlatform(t, addr, nil)
	in := p.input

	if err := in.ResetInput(ctx, platform.ResetPermissions); err != nil {
		t.Fatal(err)
	}
	if got := f.perms["kde-authorized/remote-desktop"]; !slices.Equal(got, []string{"org.other.App"}) {
		t.Errorf("remote-desktop apps = %v, want only the other app left", got)
	}
	if err := in.ResetInput(ctx, platform.ResetShortcuts); err != nil {
		t.Fatal(err)
	}
	if !f.cleaned {
		t.Error("KGlobalAccel component was not cleaned up")
	}
	if err := in.ResetInput(ctx, "bogus"); err == nil {
		t.Error("unknown reset accepted")
	}
}

func TestResetInputOlderKGlobalAccelUnregisters(t *testing.T) {
	f := &resetFake{noClean: true, hasStore: true, perms: map[string][]string{}}
	addr := startBus(t)
	f.export(t, addr)
	p := newTestPlatform(t, addr, nil)
	if err := p.input.ResetInput(context.Background(), platform.ResetShortcuts); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.unreg, []string{"hotkey-f8", "hotkey-shift-f8"}) {
		t.Errorf("unregistered %v", f.unreg)
	}
}

// Without a permission store or KDE there is nothing to undo, and no error.
func TestResetInputDegradesGracefully(t *testing.T) {
	addr := startBus(t)
	p := newTestPlatform(t, addr, nil)
	for _, what := range []string{platform.ResetPermissions, platform.ResetShortcuts} {
		if err := p.input.ResetInput(context.Background(), what); err != nil {
			t.Errorf("%s with nothing running: %v", what, err)
		}
	}
}
