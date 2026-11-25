# Linux platform

`internal/platform/linux` implements the Footman `Platform` interface on Linux:
input (hotkeys, typing, paste), launcher, notifications, paths, secrets,
windows (KDE), clipboard, audio and the systemd user service.

| Part | How |
|---|---|
| Input: hotkeys | `BindHotkeys` (setup) binds through the XDG GlobalShortcuts portal and puts each shortcut on exactly the asked-for key (ADR-0022, ADR-0029). `Hotkeys` (the daemon) registers with KDE's KGlobalAccel directly, keeping the stored key, because KDE's portal shows its dialog for every new session; the portal elsewhere (ADR-0030). Both report press and release |
| Input: typing | RemoteDesktop portal keysyms, at most 1,000 characters/s, one session while Footman runs; the restore token is kept in `<data>/remote-desktop.token` |
| Input: paste | Text offered on the clipboard through Wayland data-control (pure-Go client, `input_wayland.go`, `input_datacontrol.go`), marked so Klipper skips it; Ctrl+V, or Ctrl+Shift+V in terminals, pressed through the portal; the previous clipboard put back once the app has read it |
| Input: layout | `Typeable` (a `dictation.Layout`) reads the xkb keymap the compositor sends; `input_keysyms_table.go` is generated from `xkbcommon-keysyms.h` (`go generate`) |
| Launcher | `xdg-open` for URLs and files; `gio launch` (or `gtk-launch`) for apps, found by `.desktop` ID or name |
| Notifier | `org.freedesktop.Notifications` over D-Bus, with action buttons. `SetTray` is a stub |
| Paths | XDG folders: `~/.config/footman`, `~/.local/share/footman`, `~/.cache/footman`, `$XDG_RUNTIME_DIR` |
| Secrets | Secret Service (`org.freedesktop.secrets`): KWallet or gnome-keyring. A clear `ErrNoKeyring` if none is running |
| Windows | KWin scripting over D-Bus (KDE Plasma only; `ErrUnsupported` elsewhere) |
| Clipboard | `wl-copy` / `wl-paste` (package `wl-clipboard`) |
| Audio | `pactl` (list, watch), `parec` (capture) and `pacat` (playback) from `pulseaudio-utils`; also works on PipeWire via pipewire-pulse (ADR-0026) |
| Service | systemd user unit `$XDG_CONFIG_HOME/systemd/user/footman.service`, started with the graphical session; `systemctl --user` enables, (re)starts, stops and disables it (ADR-0030) |

Pure Go, no cgo.

## Unit tests

```sh
go test ./internal/platform/linux/
```

They start a private `dbus-daemon --session` with fake notification, keyring and
KWin services, so they need no desktop. If `dbus-daemon` is not installed the
D-Bus tests skip with a message (`apt install dbus` / `dbus-daemon`).

The input code needs a `.desktop` file for its app id (`io.github.irobinson010.Footman`), which `footman setup` installs. Its `Exec` must name a program that exists, or the portal will not accept the app id.

## Nested input tests (headless KWin, safe on a live desktop)

`input_nested_test.go` (build tag `integration`) runs Input against a private,
headless KWin started by `testdata/nested-kwin.sh`:

- `kwin_wayland --virtual` has no real input devices, so nothing typed in it reaches your desktop, and nothing you type reaches it.
- It has its own D-Bus session bus, so the portals, kglobalaccel and the permission store are private copies.
- It has its own `XDG_RUNTIME_DIR` and XDG config, data, cache and state folders, so your shortcuts, portal permissions and clipboard are never touched.

The tests connect only to that session's addresses, never to
`$WAYLAND_DISPLAY` or your session bus.

```sh
go test -tags integration -v -count=1 ./internal/platform/linux/ -run TestNested
```

They need KDE Plasma's `kwin_wayland`, `dbus-run-session` and `wl-clipboard`,
and skip a test when its app is missing:

| Test | Checks | Needs |
|---|---|---|
| `TestNestedKeymap` | the keymap arrives and `Typeable` follows it (US: `é`, `—`, emoji are not typeable) | |
| `TestNestedTypeQt` | typed text arrives exactly, under 1,000 characters/s | `python3-pyqt6` |
| `TestNestedPasteQt`, `...RestoresEmptyClipboard` | Unicode paste with Ctrl+V, previous clipboard (or none) restored | `python3-pyqt6` |
| `TestNestedPasteKonsole` | Ctrl+Shift+V in Konsole, arriving as a bracketed paste | `konsole` |
| `TestNestedChromium` | portal-sent Ctrl+V pastes into Chromium; typing too | Brave, Chromium or Chrome, or `FOOTMAN_TEST_CHROMIUM=/path` |
| `TestNestedHotkeysPortal` | GlobalShortcuts press and release; binding after a dismissed dialog; a key moved in settings; rebinding | |
| `TestNestedHotkeysKGlobalAccel` | the KGlobalAccel fallback, and that a taken key (Alt+F4) is refused | |
| `TestNestedHotkeysNoDialogAfterSetup` | after setup's binding, the daemon's registration shows no dialog and keeps a key the user moved | |

The tests answer the RemoteDesktop dialog in advance through the nested
permission store, and press Return in the GlobalShortcuts dialog. To debug,
start a session by hand with `testdata/nested-kwin.sh /tmp/fmdbg` and run the
tests with `FOOTMAN_NESTED_ROOT=/tmp/fmdbg`; they then reuse it and leave it
running. `FOOTMAN_NESTED_KEEP=1` keeps a session's folder after the run.

## End-to-end push-to-talk test (nested KWin, virtual audio)

`cmd/footman/e2e_test.go` (`TestE2EPushToTalk`, build tags `integration native`)
uses the same nested session (`nested.go`) to run the real `footman` binary.
It runs `setup --no-service` in a temporary HOME, then `daemon`. It holds F8
through the session's RemoteDesktop portal while a LibriSpeech clip plays into
a `footman_test_*` null sink, the configured microphone, and checks the text
in a Qt field. Then it measures idle memory and runs `uninstall --purge`. It
never opens a real microphone or speaker. See ADR-0030 and CONTRIBUTING.md.

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

### Real-app check for Input (issue #47)

`TestRealInsert` types and pastes into the focused window of your real
desktop, so it runs only with `FOOTMAN_REAL_INPUT=1`:

```sh
FOOTMAN_REAL_INPUT=1 go test -tags integration -v -count=1 -timeout 30m ./internal/platform/linux/ -run TestRealInsert
```

1. A "Footman … remote control" dialog appears the first time: approve it.
2. For each of Konsole, Brave, Firefox and VS Code in turn (or the apps in
   `FOOTMAN_REAL_APPS=Kate,Brave`), click into a text field within the 8-second
   countdown: a shell prompt in Konsole, a text box in the browsers, an editor
   tab in VS Code. Footman types an ASCII line and pastes a Unicode line after
   it. Neither has a newline, so nothing is sent or run. Clear the Konsole
   prompt with Ctrl+U afterwards.
3. Check that each field ends with exactly the line the test prints, and that
   the test reports the previous clipboard restored. Konsole should show the
   pasted part as a highlighted (bracketed) paste.
4. Open the clipboard history (Klipper) from the tray: the pasted line should
   not be there.

The test names itself Footman with a temporary `.desktop` file, removed at the
end. KDE keeps the approval under Footman's app id. To revoke it, run
`flatpak permission-reset io.github.irobinson010.Footman` if `flatpak` is
installed.

## Audio integration tests (virtual devices only)

```sh
go test -tags integration -v -count=1 ./internal/platform/linux/ -run TestRealAudio
```

They need a PulseAudio or PipeWire (pipewire-pulse) server and skip without one.
Each test creates its own null sink (`footman_test_*`), plays into it, records
its `.monitor` and unloads it afterwards. They never open a real microphone or
play on a real speaker. `TestRealAudioLatency` logs the producer-to-frame time.
