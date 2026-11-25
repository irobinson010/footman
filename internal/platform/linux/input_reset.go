//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

const (
	permStoreDest  = "org.freedesktop.impl.portal.PermissionStore"
	permStorePath  = dbus.ObjectPath("/org/freedesktop/impl/portal/PermissionStore")
	permStoreIface = "org.freedesktop.impl.portal.PermissionStore"
)

// permTables are the permission store tables that can hold the portal's
// RemoteDesktop and GlobalShortcuts grants for an app (KDE keeps them in
// "kde-authorized"; the others are the portal specifications' names).
var permTables = []string{"kde-authorized", "remote-desktop", "global-shortcuts"}

var _ platform.InputReset = (*Input)(nil)

// ResetInput implements platform.InputReset (ADR-0031).
func (in *Input) ResetInput(ctx context.Context, what string) error {
	switch what {
	case platform.ResetPermissions:
		return in.resetPermissions(ctx)
	case platform.ResetShortcuts:
		return in.resetShortcuts(ctx)
	}
	return fmt.Errorf("linux input: cannot reset %q", what)
}

// resetPermissions removes the app's entries from the portal permission
// store, the host-app equivalent of `flatpak permission-reset`. A desktop
// without the store, or an app without entries, has nothing to undo.
func (in *Input) resetPermissions(ctx context.Context) error {
	p, err := in.portal()
	if err != nil {
		return nil // no session bus: nothing was granted from here
	}
	store := p.conn.Object(permStoreDest, permStorePath)
	var errs []error
	for _, table := range permTables {
		var ids []string
		if err := store.CallWithContext(ctx, permStoreIface+".List", 0, table).Store(&ids); err != nil {
			if !ignorableReset(err) {
				errs = append(errs, fmt.Errorf("permission store %s: %w", table, err))
			}
			if serviceMissing(err) {
				return nil
			}
			continue
		}
		for _, id := range ids {
			err := store.CallWithContext(ctx, permStoreIface+".DeletePermission", 0, table, id, in.appID).Err
			if err != nil && !ignorableReset(err) {
				errs = append(errs, fmt.Errorf("permission store %s/%s: %w", table, id, err))
			}
		}
	}
	return errors.Join(errs...)
}

// resetShortcuts removes Footman's component from KGlobalAccel, which drops
// its entry from KDE's shortcut settings. Without KDE there is none.
func (in *Input) resetShortcuts(ctx context.Context) error {
	c, ka, err := in.kglobalaccel(ctx)
	if err != nil {
		return nil
	}
	var comp dbus.ObjectPath
	if err := ka.CallWithContext(ctx, kglobalaccelIface+".getComponent", 0, in.appID).Store(&comp); err != nil {
		if ignorableReset(err) {
			return nil // KGlobalAccel has no component for Footman
		}
		return fmt.Errorf("KGlobalAccel getComponent: %w", err)
	}
	obj := c.Object(kglobalaccelDest, comp)
	if err := obj.CallWithContext(ctx, kglobalaccelComp+".cleanUp", 0).Err; err == nil {
		return nil
	}
	// A KGlobalAccel without cleanUp: unregister each shortcut it lists.
	var names []string
	if err := obj.CallWithContext(ctx, kglobalaccelComp+".shortcutNames", 0).Store(&names); err != nil {
		return fmt.Errorf("KGlobalAccel component %s: %w", in.appID, err)
	}
	var errs []error
	for _, n := range names {
		if err := ka.CallWithContext(ctx, kglobalaccelIface+".unregister", 0, in.appID, n).Err; err != nil && !ignorableReset(err) {
			errs = append(errs, fmt.Errorf("KGlobalAccel unregister %s: %w", n, err))
		}
	}
	return errors.Join(errs...)
}

// ignorableReset reports whether err only says there was nothing to delete.
func ignorableReset(err error) bool {
	var e dbus.Error
	if serviceMissing(err) {
		return true
	}
	if errors.As(err, &e) {
		return e.Name == "org.freedesktop.portal.Error.NotFound" || e.Name == "org.freedesktop.DBus.Error.UnknownMethod"
	}
	return false
}
