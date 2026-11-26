# 0031. Uninstall undoes desktop grants; tests never reach the real session

- Status: Accepted
- Date: 2026-11-25
- Amends: 0023, 0029, 0030

## Context
Issue #60 collected three follow-ups to the Linux input work. The schema still defaulted the hotkeys to Super-based combinations although ADR-0022 avoids Meta (it opens the KDE launcher) and setup writes F8. Uninstall left the portal permission and KDE's shortcut entry behind (ADR-0030). And during #55 a plain unit test reached the owner's real session bus and opened a RemoteDesktop permission dialog.

## Decision
**Defaults.** `input.dictate.key` is `F8` and `input.command.key` is `Shift+F8`. Nothing else used Shift+F8.

**Uninstall.** Setup records two `desktop` entries in the receipt (`input-permissions`, `input-shortcuts`) when the platform's Input implements the optional `platform.InputReset`. Uninstall calls `ResetInput` for each. On Linux:
- *Permissions:* for the tables `kde-authorized`, `remote-desktop` and `global-shortcuts`, `PermissionStore.List` then `DeletePermission` for `io.github.irobinson010.Footman`. This is the host-app equivalent of `flatpak permission-reset`.
- *Shortcuts:* `KGlobalAccel.getComponent` then the component's `cleanUp`; if that is missing, `unregister` for each name in `shortcutNames`.
- A missing permission store, no KDE, no entries, or no session bus is not an error: there is nothing to undo.

**Session guard.** `internal/platform/linux/guard.go` (not built with the `integration` tag) makes `New` dial the session bus through `connectSessionBus`, and `waylandSocket` consult `realDisplayRefused`. Under `testing.Testing()`, both refuse the value the process **inherited** (`DBUS_SESSION_BUS_ADDRESS`, `WAYLAND_DISPLAY`, captured at start, empty included, since the bus library falls back to the default socket). A test that starts a private bus or nested compositor sets the variable itself, so it differs and is allowed. `FOOTMAN_TEST_REAL_SESSION=1` opts in. Tests that inject their own dialer never go through the guard.

## Consequences
- A plain `go test` can no longer pop dialogs on the developer's desktop; `TestGuard*` proves it.
- Receipts written before this change have no desktop entries, so uninstalling them leaves the grants as before.
- The permission-store table names are partly KDE's; other desktops may keep grants elsewhere, which stay.
