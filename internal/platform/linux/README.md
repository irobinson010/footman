# Linux platform

`internal/platform/linux` implements the Footman `Platform` interface on Linux:
launcher, notifications, paths, secrets, windows (KDE) and clipboard. `Input`,
`Audio` and `Service` return `platform.ErrUnsupported` until their own tasks
land.

| Part | How |
|---|---|
| Launcher | `xdg-open` for URLs and files; `gio launch` (or `gtk-launch`) for apps, found by `.desktop` ID or name |
| Notifier | `org.freedesktop.Notifications` over D-Bus, with action buttons. `SetTray` is a stub |
| Paths | XDG folders: `~/.config/footman`, `~/.local/share/footman`, `~/.cache/footman`, `$XDG_RUNTIME_DIR` |
| Secrets | Secret Service (`org.freedesktop.secrets`): KWallet or gnome-keyring. A clear `ErrNoKeyring` if none is running |
| Windows | KWin scripting over D-Bus (KDE Plasma only; `ErrUnsupported` elsewhere) |
| Clipboard | `wl-copy` / `wl-paste` (package `wl-clipboard`) |

Pure Go, no cgo.

## Unit tests

```sh
go test ./internal/platform/linux/
```

They start a private `dbus-daemon --session` with fake notification, keyring and
KWin services, so they need no desktop. If `dbus-daemon` is not installed the
D-Bus tests skip with a message (`apt install dbus` / `dbus-daemon`).

## Integration tests (the owner's KDE desktop)

These touch the real desktop, so they sit behind the `integration` build tag.
Run them from **Konsole** in a normal Plasma Wayland session, with
`wl-clipboard` installed:

```sh
go test -tags integration -v -count=1 ./internal/platform/linux/ -run TestReal
```

What to expect:

1. `TestRealOpenURL`: a browser tab opens on example.com.
2. `TestRealNotificationAction`: a notification appears with a **Confirm**
   button. Click it within 60 seconds.
3. `TestRealSecrets`: stores, reads and deletes a secret named
   `footman-integration-test`. KWallet may ask you to create or unlock the
   wallet the first time.
4. `TestRealActiveWindow`: logs the active window. It should be Konsole (an app
   ID like `org.kde.konsole`). KWin may log a one-time script-loading message.
5. `TestRealClipboard`: writes then reads the clipboard, and restores what was
   there (text only).

Run one at a time with `-run TestRealSecrets` and so on. If something fails,
paste the output into issue #41.
