# S0-06 spike: global hotkeys and typing at the cursor on KDE Wayland

Issue: [#6](https://github.com/irobinson010/footman/issues/6). Status: **automated part done; real-desktop testing pending** (see [Steps for the user](#steps-for-the-user)).

## TL;DR

- **Push-to-talk hotkey:** use the **XDG GlobalShortcuts portal**. On KDE it reports press **and** release (`Activated` / `Deactivated`), about 8 ms after the key event. There is one consent dialog the first time a shortcut id is bound. After that it binds silently. It is pure Go over D-Bus.
- **Text at the cursor:** no single method works everywhere.
  - **Keystrokes** (KWin fake-input keysyms, through the RemoteDesktop portal or directly) handle every app for characters on the current keyboard layout.
  - Characters **not on the layout** (é, —, “”, emoji, CJK on a US layout) are **silently dropped by Chromium, Electron and every Xwayland app**.
  - **Clipboard + Shift+Insert** worked in every app tested, with full Unicode, in about 25 ms whatever the length. Terminals get it as a bracketed paste.
  - So `Insert` has to **choose per utterance**: type when every character is on the layout, otherwise paste.
- `wtype` cannot work on KDE (KWin has no `zwp_virtual_keyboard_v1`). `ydotool`/uinput is a last resort. libei adds nothing over the portal's keysym calls on KDE.

## Environment

Kubuntu, Plasma/KWin **6.6.6**, xdg-desktop-portal **1.21.1**, xdg-desktop-portal-kde **6.6.6**, libei **1.5.0** (runtime only, no headers), a current kernel, Go 1.26.
Test apps: Konsole 25.12.3, Qt 6.10.2 `QPlainTextEdit` (stands in for Kate/KDE apps), GTK 4.22 `TextView`, Firefox 158 (snap), Brave 156 (Chromium), Electron 38.8 (stands in for VS Code / Teams / Slack / Discord), each on native Wayland and, where noted, on Xwayland.

Already on the machine: `wl-copy`/`wl-paste`, `kwin_wayland`, `wayland-info`, `busctl`. **Not installed:** `wtype`, `ydotool`, `dotool`, VS Code, Teams, Chrome.

Things worth knowing about this particular machine:
- `/dev/uinput` already has an ACL `user:me:rw` because the `steam-input` and `solaar` udev rules tag it `uaccess`. So uinput tools would work here without root. **A stock install would need root or a udev rule.**
- The user is not in the `input` group, so evdev cannot be read.

## How it was tested (and what was not)

All live input went into a **private, headless KWin**: `kwin_wayland --virtual` on its own Wayland socket, inside `dbus-run-session`. It had its own portal, kglobalaccel and permission store, and `XDG_CONFIG_HOME`/`XDG_DATA_HOME`/`XDG_CACHE_HOME`/`XDG_STATE_HOME` pointed at a scratch directory ([`nested/run.sh`](nested/run.sh)). A virtual KWin has no libinput seat, so nothing typed there can reach the real desktop, and the real `kglobalshortcutsrc` and portal permissions were never touched. Test apps ran inside it and logged every text change with a timestamp ([`sink/`](sink), [`cmd/websink`](cmd/websink)). [`cmd/analyze`](cmd/analyze) then compared the logged text with the sample and computed characters per second.

- Samples: [`testdata/ascii.txt`](testdata/ascii.txt) (480 characters of prose and symbols) and [`testdata/unicode.txt`](testdata/unicode.txt) (90 characters: é à ï — “” ‘’ … £ € ¥ ½ ß, Greek, Cyrillic, CJK, Hangul, emoji, ✓). The nested keymap is the default US layout.
- Hotkey presses inside the nested session were synthesised with the fake-input client. That exercises KWin's real shortcut path (`GlobalShortcutFilter` → kglobalaccel → portal), but not physical keyboard timing.
- **Not run:** anything on uinput (ydotool) or evdev, because uinput devices reach every session and evdev needs the `input` group. Nothing was run on the real desktop, and nothing was tested in real VS Code, Teams or Chrome. Those tests are listed under [Steps for the user](#steps-for-the-user).

Speeds are receiver-side: characters ÷ (last change − first change). "Paste" latency is the time from the start of the command to the text appearing.

## Hotkeys (push-to-talk)

| Method | Works on KDE Wayland | Press + release | Permission | Latency | Caveats |
|---|---|---|---|---|---|
| **XDG GlobalShortcuts portal** ([`cmd/gshortcuts`](cmd/gshortcuts)) | **Yes** (tested, nested) | **Yes**: `Activated` / `Deactivated`. No repeat events while held. | Dialog the first time each new shortcut id is bound ([screenshot](img/globalshortcuts-dialog.png)). The user can change the key there. Later binds return in about 1 ms with no dialog. | ~8 ms (includes spawning the injector) | `timestamp` is always 0 on KDE, so use our own clock. Releasing a modifier first ends the press (good for PTT). The key is consumed and does not reach the focused app. Active only while our session is open. Needs an app id (see below). |
| **KGlobalAccel D-Bus** ([`cmd/kglobalaccel`](cmd/kglobalaccel)) | **Yes** (tested, nested) | **Yes**: `globalShortcutPressed` / `Released`, plus `Repeated` at ~25 Hz after 600 ms | None, and no dialog | ~8 ms | KDE only. It is the private API behind the KGlobalAccel library, though it has been stable for years. Silently takes the key unless we check for conflicts. Shows up in System Settings → Shortcuts. We must call `setInactive` on exit. |
| **evdev** (`/dev/input/event*`, [`cmd/evdevptt`](cmd/evdevptt)) | Not tested (needs the `input` group) | Yes in principle (value 1 / 2 / 0 = press / repeat / release) | `input` group or root | — | Can read every keystroke on the machine, in every session and on the lock screen: keylogger-level access. Does not consume the key, so the focused app gets it too. Works on any compositor and on X11. Ignores shortcut inhibitors. |

Findings:
- **App identity matters.** An unsandboxed process that does not identify itself is attributed by xdg-desktop-portal to whatever systemd scope it runs in. Our first RemoteDesktop dialog read "**Konsole** is asking for special privileges" ([screenshot](img/remotedesktop-dialog-unregistered.png)). Footman must install a `.desktop` file and call `org.freedesktop.host.portal.Registry.Register(app_id)` as its first portal call. After that the dialog reads "Footman … is asking" ([screenshot](img/remotedesktop-dialog-registered.png)) and grants are keyed to Footman. Without an app id, KDE files shortcuts under the component `token_<session_handle_token>`.
- **Trigger choice:** Ctrl+Alt+F1…F12 are VT-switch keys (Ctrl+Alt+F9 never reached kglobalaccel), and Meta+F9 is already KWin's "Expose". Both are useless. Pause, ScrollLock and Ctrl+Shift+Space all worked. The portal dialog shows conflicts, but the KGlobalAccel path does not.
- KWin skips global shortcuts while a window holds a keyboard-shortcuts inhibitor, for example a VM or remote-desktop client in focus (`GlobalShortcutFilter` in `src/input.cpp`). Only evdev bypasses that.
- Hold-to-talk and tap-to-toggle can both be built on press/release. The portal's lack of repeat events makes hold detection simpler.

## Text at the cursor

### Methods

| Method | Works on KDE Wayland | Permission | Unicode | Speed | Notes |
|---|---|---|---|---|---|
| **RemoteDesktop portal, `NotifyKeyboardKeysym`** ([`cmd/rdportal`](cmd/rdportal)) | **Yes** | One dialog ("Control input devices", with "Allow restoring on future sessions" ticked). After that the restore token (`persist_mode=2`) brings the session back in about 5 ms with no dialog. Or skip the dialog completely with KDE's `kde-authorized` permission (tested). | Any keysym: KDE forwards it to KWin fake-input, which temporarily swaps in a keymap containing the symbol. **Chromium, Electron and Xwayland drop symbols that need the swap.** | 3,500–4,400 cps (paced by D-Bus round trips) | Pure Go, cross-desktop (GNOME implements it too). A restored session fires a "Remote control session started" notification, and a "Remote Control" tray item is visible while the session is open (both from the xdp-kde source; to be confirmed on the real desktop). |
| **KWin `org_kde_kwin_fake_input` directly** ([`cmd/kwinfake`](cmd/kwinfake), pure-Go Wayland client) | **Yes** | **No prompt and no root.** KWin only offers the global to executables whose `.desktop` file lists it in `X-KDE-Wayland-Interfaces` ([`nested/install-desktop.sh`](nested/install-desktop.sh)), matched on the canonical binary path. | Same keymap-swap behaviour as above | 6,000–25,000 cps unpaced; ~890 cps with 1 ms pacing | KDE only. KWin authenticates any client that gets the global (`// TODO: make secure`), so KDE could tighten this. The `.desktop` file must follow the binary if it moves. This is what the portal itself uses on KDE. |
| **RemoteDesktop portal + libei** (`ConnectToEIS`, [`cmd/rdeis`](cmd/rdeis), cgo) | **Yes** (tested in Qt) | Same dialog and token as the portal row | **Only characters on the current layout.** EI sends evdev keycodes and the keymap belongs to the compositor. | ~7,000 cps | Needs cgo (Ubuntu ships `libei.so.1` without headers; the spike declares the prototypes itself) or a pure-Go EI implementation. An unpaced burst lost its tail when the session closed before libei flushed. Gives nothing over keysyms on KDE. |
| **`wtype`** (`zwp_virtual_keyboard_v1`) | **No** | — | — | — | KWin 6.6.6 does not implement the protocol. It is absent from the globals of both the real session and the nested one, and absent from KWin's source. Not installed, and no need to install it. Relevant only for wlroots compositors. |
| **`ydotool` / uinput** | Not tested (uinput reaches every session) | Root, the `input` group, or a udev rule (this machine happens to have a uaccess ACL) plus the `ydotoold` daemon | Layout-bound keycodes (`ydotool type` assumes US) | — | Works on any compositor and on X11. Last resort. |
| **Clipboard + simulated paste** ([`nested/paste.sh`](nested/paste.sh): `wl-copy` over `ext_data_control_v1`, then a key combo through fake-input) | **Yes** | Only what the key injector needs | **Full** | **22–36 ms** for 480 or 90 characters | Clobbers the clipboard. We save it and restore it 300 ms later, which every tested app tolerated. Clipboard managers (Klipper) will record the text unless it is offered with `x-kde-passwordManagerHint: secret` (not tested). In X11 terminals such as xterm, Shift+Insert pastes PRIMARY. |

### Method × app matrix

Each cell is the tested result for the ASCII and Unicode samples. ✅ = arrived exactly, ⚠️ = arrived only with a workaround, ❌ = failed.

| App | fake-input keysym (unpaced) | fake-input keysym, 1 ms pacing | Portal `NotifyKeyboardKeysym` | libei | Paste: Shift+Insert | Paste: Ctrl+V | Paste: Ctrl+Shift+V |
|---|---|---|---|---|---|---|---|
| Konsole (Wayland, raw tty) | ✅ 22,600 cps / ✅ 1,130 | — | ✅ 4,240 / ✅ 970 | — | ✅ bracketed | ❌ inserts `^V` | ✅ bracketed |
| Qt widget (Wayland; Kate stand-in) | ✅ 11,700 / ✅ 1,100 | — | ✅ 4,440 / ✅ 780 | ✅ ~7,000 / ❌ 38 of 90 untypeable | ✅ | ✅ | ❌ nothing |
| GTK 4 (Wayland) | ✅ 6,600 / ✅ 1,140 ¹ | — | ✅ 3,950 / ✅ 960 | — | ✅ | ✅ | ❌ nothing |
| Firefox 158 (Wayland) | ⚠️ **characters reordered** / ✅ 750–790 | ✅ 890 (6/6 runs) / ✅ | ✅ 3,700 / ✅ 850 | — | ✅ | ✅ | ✅ |
| Brave 156 / Chromium (Wayland) | ✅ 10,100 / ❌ **non-layout characters dropped** | ❌ (30 ms pacing did not help) | not run (same engine as Electron) | — | not run | not run | not run |
| Electron 38 (Wayland) | ✅ 6,400 / ❌ dropped | — | ✅ 3,580 / ❌ dropped | — | ✅ | ✅ | ✅ |
| Electron 38 (Xwayland) | ✅ 6,700 / ❌ dropped | ✅ 890 / ❌ | — | — | ✅ | ✅ | ✅ |
| Qt widget (Xwayland) | ✅ 13,200 / ❌ dropped | ✅ 890 / ❌ (50 ms pacing did not help) | — | — | ✅ | ✅ | — |

"Dropped" means every character that is not on the US layout vanished, while the ASCII characters around it arrived. For example, `Café déjà vu — naïve “quotes”` arrived as `Caf dj vu  nave quotes`.

¹ The first Unicode burst into a freshly opened GTK window took about 2 s to appear. Later bursts took 0.08 s.

### Other caveats

- **Focus:** keystrokes go to whatever is focused when they arrive. The portal's first-run dialog takes focus, and KWin returned focus to the previous window once the dialog was approved. Footman should insert only after the consent step and should re-check the active window right before inserting.
- **Terminals:** a typed `\n` is Enter, so dictated text containing a newline would **run a command**. Paste with Shift+Insert or Ctrl+Shift+V reaches Konsole as a bracketed paste (`ESC[200~…ESC[201~`), which shells do not execute. Ctrl+V in a terminal is quoted-insert, not paste.
- **Chromium on a headless test seat:** Chromium ignored all keys until a keyboard existed on the seat when it started (we kept an idle fake-input client connected). A real desktop always has a keyboard, so this is a test artifact.
- **Firefox and burst speed:** unpaced fake-input at 25,000 cps produced a 38-character chunk out of order in 2 of 2 runs. Pacing at 1 ms per character, or the portal's natural rate of ~3,700 cps, was clean. Typing should be rate-limited (default ≤ 1,000 cps).
- **Harness artifact:** a few runs first reported "no records". The cause was a `wl-copy` daemon holding the benchmark's stdout pipe, and checking the logs afterwards showed the text had always arrived. [`nested/paste.sh`](nested/paste.sh) now detaches it. None of the results above depend on those runs.

## Recommendation for the Linux `Input` implementation

**Hotkeys: `RegisterHotkey(id, description, preferredTrigger) → <-chan {Pressed | Released}`**
1. **GlobalShortcuts portal** (default). Register an app id first, keep the session open for the daemon's lifetime, ignore the portal's timestamp, and default to a single key such as Pause or ScrollLock, or Ctrl+Shift+Space. Works on KDE, GNOME 48+ and Hyprland.
2. **KGlobalAccel D-Bus**, only on Plasma where the portal lacks GlobalShortcuts (Plasma < 5.27). Check `globalShortcutAvailable` first.
3. **evdev**, opt-in only, for setups with neither (wlroots and others). It needs the `input` group, and the setup wizard should explain the security cost. The wake word and the tray icon remain the other ways to start.

**Text: `Insert(ctx, text, mode)` with `mode = auto | type | paste` (config, default `auto`)**
- `auto` = **paste** when the text contains any character that is not on the active layout, contains a newline, or is longer than about 300 characters. Otherwise **type**. That gives exact Unicode everywhere, no accidental command execution in terminals, and no clipboard side effects for short plain dictation.
- **Typing backend**, in fallback order:
  1. **RemoteDesktop portal `NotifyKeyboardKeysym`**: pure Go and cross-desktop. One consent dialog, then a restore token stored in Footman's state dir. Hold one session open while the daemon runs (avoids a notification per utterance), and rate-limit to ≤ 1,000 cps.
  2. **KWin fake-input directly** (KDE): no dialog and no tray item. The installer writes a `.desktop` file with `X-KDE-Wayland-Interfaces=org_kde_kwin_fake_input` pointing at the installed binary. Use it if the portal's notification or tray item proves annoying in real use, or the portal is unavailable.
  3. **uinput** (our own Go writer to `/dev/uinput`, no `ydotoold`): last resort for compositors without either. Needs a udev rule (documented, never added silently), and only layout characters can be typed.
  - Skip `wtype` (KWin lacks the protocol; keep it as a possible future wlroots backend) and libei (keycodes only on KDE; revisit only if a desktop drops keysym support).
- **Paste backend:** set the clipboard over `ext_data_control_v1` (pure Go; fd passing is needed, or shell out to `wl-copy` at first). Then send **Shift+Insert**, the one combo that worked in every app tested, terminals included. Restore the previous clipboard after about 300 ms, and offer the text with `x-kde-passwordManagerHint: secret` so Klipper skips it.
- **Layout check:** fetch the keymap (fake-input or `wl_keyboard`) with xkbcommon-style keysym lookup, so `auto` knows which characters are typeable. Until that exists, treat anything outside printable ASCII as "paste".

Proposed ADR text: *"Linux input: GlobalShortcuts portal for hotkeys; RemoteDesktop portal keysyms (KWin fake-input fallback) for short layout-only text; clipboard + Shift+Insert for everything else."*

## Tested here vs. pending

| Area | Tested in the nested KWin | Needs the user on the real desktop |
|---|---|---|
| GlobalShortcuts portal: bind dialog, persistence, press/release, modifier release, key consumed | ✅ | Physical keyboard; System Settings entry; behaviour in a fullscreen game or VM |
| KGlobalAccel direct | ✅ | Physical keyboard |
| evdev | code only | ✅ run with sudo once |
| Portal keysym typing, restore token, KDE `kde-authorized` grant | ✅ | Notification and tray item on each session; real Chrome, VS Code, Teams |
| fake-input `.desktop` authorisation | ✅ | Same check on the real KWin |
| libei | ✅ Qt only | optional |
| Clipboard + paste | ✅ | Klipper history; real VS Code and Teams (Ctrl+Shift+V there may mean something else) |
| ydotool | ❌ (unsafe to run here) | optional |

## Steps for the user

Everything below runs on your real desktop and injects real keystrokes into whatever window has focus. Each typing command counts down 5 seconds; click into the target text field before it finishes. The samples contain no newline, so nothing gets sent or executed, but **do not press Enter** in chat apps. For Signal or Discord, use a draft or "Note to Self".

1. Build the spike:
   `cd spikes/wayland-input && ./real-desktop.sh build`
2. Let the spike binaries identify themselves to the portal and to KWin. This writes four `org.footman.spike.*.desktop` files into `~/.local/share/applications`; step 12 removes them.
   `./real-desktop.sh authorise`
   Then confirm KWin offers fake-input to the spike: `bin/kwinfake -list | grep fake_input` should print `org_kde_kwin_fake_input v6`. If it doesn't, wait 10 s and retry.
3. **Push-to-talk via the portal:** run
   `bin/gshortcuts -app org.footman.spike.gshortcuts -id ptt -trigger Pause -for 60s`
   Click **OK** in the "Global Shortcuts Requested" dialog. Then, with focus in different apps (Konsole, Firefox, a fullscreen video), hold Pause for about 1 s and release, and tap it once. Each press should print an `Activated` line and each release a `Deactivated` line. Note any missing line. Run the same command again: there should be no dialog. Check that System Settings → Keyboard → Shortcuts lists "Footman spike (gshortcuts)".
4. **Push-to-talk via KGlobalAccel:** `bin/kglobalaccel -key ScrollLock -for 30s`, then hold and release ScrollLock. Expect `globalShortcutPressed`, some `Repeated` lines, then `Released`.
5. **evdev** (optional, root only for this one run): `sudo bin/evdevptt -key 119 -for 20s`, then hold and release Pause. Expect `press`, `repeat` and `release` lines tagged with your keyboard's name.
6. **Portal typing.** For each of Konsole, Kate, Firefox, Brave/Chrome, VS Code and Teams (Signal or Discord can stand in for the last two), run:
   `./real-desktop.sh type portal unicode`, then `./real-desktop.sh type portal ascii`
   The first run shows "Footman spike (rdportal) is asking for special privileges": leave "Allow restoring" ticked and click **Approve**. For each app, note whether the text matches the sample exactly, and say whether you see a "Remote control session started" notification or a "Remote Control" tray icon on each run.
7. **Direct fake-input typing:** repeat step 6 with `./real-desktop.sh type fake unicode` and `./real-desktop.sh type fake ascii`. No dialog is expected.
8. **Paste:** in the same apps, run `./real-desktop.sh type paste-si unicode` (Shift+Insert). In Konsole, VS Code and Teams also run `paste-csv` (Ctrl+Shift+V). Afterwards, open Klipper's history from the tray clipboard icon and note whether the sample text appears there.
9. **Browser numbers** (optional): run `bin/websink -log /tmp/footman-web.jsonl &` and open `http://127.0.0.1:8799` in Firefox and in Chrome/Brave. In each, click the text box, run `./real-desktop.sh type portal ascii`, then run `bin/analyze -log /tmp/footman-web.jsonl -expect-file testdata/ascii.txt -since $(( $(date +%s%3N) - 10000 ))` and paste the output line into the issue. Stop the server with `kill %1`.
10. **libei** (optional): `./real-desktop.sh type eis ascii` into Kate (approve one more dialog).
11. **ydotool** (optional; uinput affects every session, so close anything sensitive):
    `sudo apt install ydotool`, then `ydotoold &` (no sudo needed on this machine thanks to the uinput ACL), then `sleep 5; ydotool type 'Café déjà vu — naïve'` and focus Kate. Note what arrives. Then `kill %1; sudo apt remove ydotool`.
12. **Clean up:** `./real-desktop.sh cleanup`. This removes the `.desktop` files, unregisters the KGlobalAccel actions and resets the portal permissions and restore tokens for the spike app ids. Then remove the "Footman spike (gshortcuts)" entry in System Settings → Keyboard → Shortcuts if it is still listed.
13. Post the results as a comment on issue #6 (a ✅/❌ per app and method is enough), especially for steps 3, 6 and 8.

## Files

- `cmd/gshortcuts`: GlobalShortcuts portal client (press/release printer)
- `cmd/kglobalaccel`: direct KGlobalAccel registration
- `cmd/evdevptt`: evdev key watcher (read-only, no grab)
- `cmd/rdportal`: RemoteDesktop portal keysym typer (pure Go)
- `cmd/rdeis`: RemoteDesktop + libei typer (cgo)
- `cmd/kwinfake`: pure-Go Wayland client for `org_kde_kwin_fake_input` (type, combos, hold)
- `cmd/websink`, `sink/*.py`, `sink/electron`: logging text sinks (browser, Qt, GTK, terminal, Electron)
- `cmd/analyze`, `sink/diff.py`: compare sink logs with the expected text and compute speed
- `nested/`: isolated headless KWin session (`run.sh`, `in.sh`), window activation, benchmark loop, paste helper, `.desktop` authoriser
- `real-desktop.sh`: wrapper for the user steps
- `internal/wl`: minimal pure-Go Wayland wire client; `internal/portal`: portal request plumbing; `internal/keys`: rune → keysym and evdev codes

Spike code is throwaway and must not be imported from `cmd/` or `internal/` at the repo root.

## Real-desktop results (owner, 2026-11-24)

Tested on the owner's KDE Plasma Wayland desktop. Brave was running natively on Wayland (`--ozone-platform=wayland`).

| Test | Result |
|---|---|
| GlobalShortcuts portal, hold and release | ✅ `Activated` / `Deactivated` arrive with another app focused. |
| Portal shortcut binding UX | ⚠️ `BindShortcuts` returned immediately and **saved the suggested trigger (`Pause`) without a dialog**. The owner's keyboard has no Pause key, so the binding was unusable until it was changed in System Settings → Shortcuts. Rebinding to F8 worked. |
| Meta-based combos | ❌ Pressing and releasing Meta opens the KDE launcher/search, so Meta combos are poor push-to-talk keys. |
| Typing via portal (`unicode`), Konsole | ✅ Exact match, all characters. |
| Typing via portal (`unicode`), Brave | ⚠️ Characters not on the layout were dropped, as predicted: `Caf dj vu  nave quotes single  20 5 3   Strae`. |
| Paste via KWin fake_input + Shift+Insert, Konsole | ❌ Only `2~` appeared (Shift+Insert pastes the *selection* in Konsole). |
| Paste via KWin fake_input + Ctrl+Shift+V, Konsole | ✅ Exact match, shown as a bracketed (highlighted) paste that isn't executed. |
| Paste via KWin fake_input + Shift+Insert / Ctrl+V, Brave | ❌ Nothing, even with `RESTORE_AFTER=3`. |
| Manual Ctrl+V in Brave after `wl-copy` | ✅ Works, so the clipboard is fine. **Brave ignores key combos injected through KWin fake_input**, while it accepts portal-injected keys. |

### Conclusions for the Linux `Input` implementation
1. **Hotkeys:** GlobalShortcuts portal. Footman must **not rely on the suggested trigger**. Setup asks the user to press their key (default suggestion **F8**), and docs explain rebinding in System Settings. Avoid Meta-based defaults.
2. **Typing:** RemoteDesktop portal keysyms, used only for text whose characters are all on the current layout.
3. **Paste (everything else):** set the clipboard through data-control, then send the paste key **through the RemoteDesktop portal**, not KWin fake_input. Use the paste combo for the app class: **Ctrl+Shift+V for terminals**, **Ctrl+V otherwise**. Restore the previous clipboard only after the app has read it.
4. KWin fake_input is a fallback only for apps where the portal is unavailable. Portal-sent paste combos in Chromium still need to be verified in Sprint 1.
