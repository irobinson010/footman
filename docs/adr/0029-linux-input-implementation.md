# 0029. Linux input: app identity, hotkey binding, clipboard handling and layout check

- Status: Accepted
- Date: 2026-11-25
- Amends: 0022

## Context
ADR-0022 chose the GlobalShortcuts portal for hotkeys, RemoteDesktop portal keysyms for typing, and clipboard plus a portal-sent paste key for everything else. Building it (S1-04) raised questions that ADR leaves open:
- Who the portals think Footman is.
- How setup makes sure the push-to-talk key is the one the user chose, since KDE may store the suggested key silently or keep an empty binding after a dismissed dialog.
- How long the dictated text stays on the clipboard.
- How "on the layout" is decided.
- Whether portal-sent paste keys work in Chromium, which the spike could not answer.

The nested KWin tests (`internal/platform/linux/input_nested_test.go`) answered the last one: Brave accepted Ctrl+V sent through the RemoteDesktop portal, and pasted Unicode text exactly.

## Decision
- **App identity.** Footman's app id is `io.github.irobinson010.Footman`. Setup writes `$XDG_DATA_HOME/applications/io.github.irobinson010.Footman.desktop`, whose `Exec` names the installed binary, and records the file in the install receipt (program group). Input calls the portal's host `Registry.Register` first on a private D-Bus connection. The portal only accepts the id if the `.desktop` file exists and its program exists. Dialogs then name Footman, and grants and shortcuts are stored under its id.
- **Shortcut ids come from the combination.** `F8` is shortcut `hotkey-f8`. Hotkey events always carry the combination from the config, even if the user has moved the shortcut to another key in the desktop's settings. The desktop owns the actual key.
- **Binding is explicit.** `platform.InputSetup` is a new optional extension of `Input`. Setup calls its `BindHotkeys` after asking for the key, and calling it again rebinds. It binds through the portal and then checks the trigger the portal reports. If the trigger isn't the requested key, Footman moves the shortcut there through KGlobalAccel's `setForeignShortcutKeys` on KDE. That is the call System Settings makes, and it first checks that no other component uses the key. On other desktops with GlobalShortcuts version 2, Footman opens the desktop's own shortcut dialog (`ConfigureShortcuts`). The daemon's `Hotkeys` only binds; it never moves a key the user chose in the desktop's settings. If a binding fails, setup still finishes and reports why.
- **KGlobalAccel fallback.** Without a GlobalShortcuts portal, hotkeys register directly with KGlobalAccel under the same component and ids. A key another component already uses is refused with that component's name, never taken silently. Footman marks its actions inactive when it stops, so the keys go back to other apps.
- **One RemoteDesktop session while Footman runs.** It is keyboard only, with `persist_mode` 2. Each restore token is single use, so the new one is written to `<data>/remote-desktop.token` (mode 0600) after every start. The receipt lists it in the data group. If the user stops the session from the tray, the next insertion starts a new one with the token, so no dialog appears. Typing is capped at 1,000 characters/s. Each character is sent as the keysym the current layout uses for it, such as `EuroSign` rather than the Unicode keysym. That way KWin never swaps in a temporary keymap for a character that is on the layout.
- **Paste keys are keysyms, not keycodes:** Control_L (plus Shift_L for terminals) and `v`. This works on non-QWERTY layouts too. The terminal table matches the focused window's app id or class, either the whole name or its last dot-separated part (`org.kde.konsole` → `konsole`). An unknown focus gets Ctrl+V.
- **Clipboard handling** goes through a small pure-Go Wayland client over `ext_data_control_v1`, with `zwlr_data_control_v1` as the fallback. No `wl-copy` is involved, because it can't offer the password hint next to the text or report reads.
  1. Save every MIME type of the current selection: at most 1 s per type and 64 MiB in total.
  2. Offer the text as `text/plain;charset=utf-8`, `text/plain`, `UTF8_STRING`, `STRING` and `TEXT`, plus `x-kde-passwordManagerHint: secret` so Klipper skips it.
  3. Wait 40 ms, then press the paste keys. Reads during the wait come from clipboard managers and are ignored.
  4. Wait for the app's first read of a text type, then for 150 ms without reads, or 1.5 s if no read comes.
  5. Put the saved selection back, or clear it if it was empty or could not be read. Restoring never leaves the dictated text behind.

  If anything else takes the clipboard during the paste, Footman leaves it alone. The restored clipboard is served by Footman's process, as `wl-copy` serves its own, so it lasts while the daemon runs.
- **Layout check.** Input implements `dictation.Layout` from the xkb keymap the compositor sends to a `wl_keyboard`. Footman parses the `xkb_symbols` section and maps each layout group to the characters its keys type at any level. On KDE, `org.kde.keyboard` reports which group is active. Elsewhere Footman assumes the first group. Without a keymap, it falls back to printable ASCII. A table generated from `xkbcommon-keysyms.h` turns keysyms into characters. Core uses the platform Input's check when the Input implements one, and `dictation.ASCIILayout` otherwise.
- **The Chromium fallback in ADR-0022 is not needed.** Portal-sent paste keys reached Brave in the nested session. The owner's check on the real desktop confirms it before this is final.

## Consequences
- Setup leaves behind a `.desktop` file, a token file and, on KDE, a shortcut entry. Uninstall removes the two files through the receipt. The shortcut entry stays in System Settings until it is removed there; uninstall doesn't reset portal permissions or the desktop's shortcut store yet.
- Moving the binary requires running setup again, because the `.desktop` file must point at an existing program for the portal to accept the app id.
- The RemoteDesktop session shows KDE's "remote control" indicator while Footman runs. If that turns out to be annoying, the fallback from ADR-0022 (KWin fake_input) is the next step.
- Clipboard contents restored by Footman go away if the daemon exits, as with `wl-copy`.
- `platform.InputSetup` is optional, so the Windows and macOS platforms can skip it or implement it later.
