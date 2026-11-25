# 0022. Linux input: portal hotkeys, portal typing, per-app paste

- Status: Accepted
- Date: 2026-11-24

## Context
Spike S0-06 (`spikes/wayland-input/REPORT.md`) tested global hotkeys and text insertion on KDE Plasma Wayland, first in a nested KWin session and then on the owner's real desktop. Findings:
- The GlobalShortcuts portal delivers press and release.
- Typed keysyms drop characters that aren't on the keyboard layout in Chromium, Electron, and Xwayland apps.
- Brave ignores key combos injected through KWin fake_input.
- Konsole's Shift+Insert pastes the selection, not the clipboard.
- `wtype` can't work on KWin.

## Decision
- **Hotkeys:** the XDG **GlobalShortcuts portal**, with KGlobalAccel as a fallback on older Plasma and evdev as an opt-in. Setup asks the user to press their push-to-talk key (default suggestion **F8**) rather than relying on the suggested trigger, because KDE may save the suggestion silently. Defaults avoid Meta-based combos, since Meta opens the launcher.
- **Insertion modes:** `auto` (default), `type`, and `paste`. In `auto`, Footman types text that is short, has no newline, and uses only characters on the current layout; everything else is pasted.
- **Typing:** RemoteDesktop portal keysyms, at most about 1,000 characters/s.
- **Paste:** set the clipboard through the Wayland data-control protocol, then send the paste combo **through the RemoteDesktop portal**. Use **Ctrl+Shift+V for terminals** and **Ctrl+V for other apps**, chosen by the focused app's class. Restore the previous clipboard after the app has read it, and mark the text so clipboard history doesn't keep it.
- **Fallbacks:** KWin fake_input, then uinput.

## Consequences
One portal permission prompt on first use, after which a saved token is reused. Footman needs a small table of terminal app classes. Sprint 1 must verify that portal-sent paste combos work in Chromium-based apps; if they don't, Chromium apps fall back to typing plus a notice about dropped characters.
