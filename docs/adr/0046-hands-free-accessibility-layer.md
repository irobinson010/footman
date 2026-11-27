# 0046. Hands-free control: the accessibility layer

- Status: Proposed
- Date: 2026-11-27
- Amends: 0003 (adds an optional `platform.Accessibility`), 0015 (the untrusted-content rule now covers actions that drive the screen)
- Related: 0022, 0029 (Linux input), 0032 (dictation formatting), 0038, 0039 (permissions), 0043 (system context), 0044 (web answers), 0045 (voice-started dictation)

## Context
The owner's goal is to use the computer without touching the mouse or keyboard, while keeping both available. Footman can already dictate, open apps and sites, change settings and run jobs. It cannot yet act on what is on the screen. The concrete gaps:

- Edit text the owner is looking at: "find *the budget*", "replace *this* with *that*", "after this sentence write …", "go to the end and write …".
- Press things: "click Send", "press the Save button", "show numbers" then "click 7", "scroll down".
- Move around: "switch to Brave", "press control T".

Dictation (ADR-0032, ADR-0045) only inserts text at the caret, and only knows what Footman itself typed. Everything above needs to know what is on the screen, which means reading other applications' content. That is the most sensitive thing Footman will do: screen text can hold passwords, mail, banking pages and prompt injections (design §29). So the design has to decide how Footman sees the screen and how much of it each part of the system may see.

Wayland gives no way to read or drive other windows (no global screen reads, no synthetic pointer without a portal), so a screen-reader-style accessibility API is the only portable, structured route. Linux's is **AT-SPI2**. Windows (UI Automation) and macOS (AXUIElement) have equivalents with the same ideas, so the abstraction is chosen to fit all three.

Two other agents are building parts of the same goal and are **not** designed here: web answers (ADR-0044) and voice-started dictation with key-based navigation and a stop word (ADR-0045). ADR-0045 leaves a seam for text-aware edits; this ADR is what plugs into that seam.

## Decision

### 1. Shape: one optional platform interface, one pure-Go editing core, one grammar

- **`platform.Accessibility`** is a new optional extension of `Platform`, found by type assertion like `SystemInfo` and `VolumeControl` (ADR-0021). It models *elements with capabilities*, not AT-SPI: find the focused element, list the actionable elements of the active window, read and change text, press, focus, scroll. Offsets are Unicode code points (Go runes), and each backend converts from its own unit (AT-SPI counts characters, UI Automation and macOS count UTF-16 units). Linux implements it first (`internal/platform/linux/atspi`); Windows and macOS follow later behind the same interface (section 8). There is a `fake` for tests.
- **`internal/textedit`** is pure Go with no desktop: it normalizes text, builds a virtual buffer from several text-bearing elements, finds phrases, resolves "this sentence" and ordinals, and turns a spoken edit into a plan of ranges. It is the part that has to be good, and it is unit-testable with no AT-SPI, no D-Bus and no microphone.
- **`internal/handsfree`** holds the command grammar (simple tier, deterministic, no AI) and the base tools that call `textedit` and `platform.Accessibility`. The tools are compiled in behind the ordinary `plugins.Tool` interface with fast-path phrases (design §24), and the smart tier exposes the same tools to the AI, with the limits in section 9.
- **Deterministic first, AI second.** "replace *brown* with *red*" needs no AI: Footman parses it, reads the field locally, edits, and nothing leaves the machine. The AI is for what rules cannot do ("replace the sentence about the budget"), and then it sees only the bounded, gated snippets of section 9.

### 2. Linux: AT-SPI2

**Connecting.** The daemon asks the session bus for the accessibility bus address (`org.a11y.Bus.GetAddress`) and connects with the godbus library it already uses. The bus is a separate dbus-daemon started on demand by `at-spi-bus-launcher`. Connecting is lazy, on the first hands-free command, so idle memory (design §2) is unchanged.

**Switching accessibility on.** Toolkits only build their accessibility trees when the bus says accessibility is enabled (`org.a11y.Status.IsEnabled`, and `ScreenReaderEnabled` for some apps). While Footman runs hands-free it sets `IsEnabled` the way Orca does, and clears it on exit if it set it. Apps started afterwards expose themselves; running GTK, Qt and Chromium apps listen for the change. This is a session-wide switch with a small cost (some toolkits do extra work for every widget), so it is a setting (`handsfree.accessibility = "auto" | "on" | "off"`, default `auto`: on only while the layer is used) and setup says so.

**Interfaces used.**

| Interface | Used for |
|---|---|
| `Accessible` | role, name, description, states (focused, editable, showing, multi-line, …), parent and children |
| `Text` | `GetText`, caret (`GetCaretOffset`, `SetCaretOffset`), selection (`GetSelection`, `SetSelection`, `AddSelection`), `GetStringAtOffset` (granularity), scrolling a range into view |
| `EditableText` | `SetTextContents`, `InsertText`, `DeleteText`, `CopyText`, `CutText`, `PasteText` (see section 3 for when) |
| `Action` | `DoAction`, `GetName` ("click", "press", "activate") |
| `Component` | `GetExtents`, `GrabFocus`, `ScrollTo`, hit testing (for the overlay and pointer fallback) |
| `Value`, `Selection` | sliders and list or combo choices ("set the volume slider to half", "pick the second one") |
| `Collection` | query by role and state in one call where the toolkit offers it, else a bounded breadth-first walk |
| `Document` | the document element of a browser page or office file, as the scope for find |

**Which element is focused.** AT-SPI has no "get the focused element" call. Footman subscribes to **focus and window-activation events only**, and keeps the latest focused element in memory. At the first command it finds the active window with the existing KWin-backed `Windows.Active` and searches that application's tree once for the focused state. It **never subscribes to text-changed or key events**: Footman has no keylogger and no stream of what the owner types, and the caret is read on demand.

**Virtual buffer.** Apps do not expose one big text. LibreOffice Writer exposes one accessible per paragraph; a browser page is a tree of text nodes; an editable web root has embedded-object placeholders (U+FFFC) for non-text children. `textedit` flattens the text-bearing descendants of the focused document (or the focused field when it is a plain field) into one virtual buffer with a map from buffer offset to (element, offset). Finding crosses element boundaries and an edit maps back to one or more per-element operations. The buffer is built for one command and dropped; it is capped (default 1 MiB and 300 ms of walking) and says so when it is cut off.

**Wayland geometry.** Under Wayland an app does not know where its window is on screen, so `GetExtents` is only reliable in *window* coordinates. Footman combines it with the window geometry from KWin to place the overlay (section 4). Where the two do not line up, numbers are placed by walking order and the click uses `Action`, not a pointer.

### 3. Text-aware editing

All of these work on the focused text element, or on the focused document when the command says so ("in the document", "on the page").

- **Read.** The focused element's text, caret and selection. A multi-line editor, a single-line field and a document are handled alike through the virtual buffer.
- **Find.** A phrase, matched in tiers, stopping at the first tier that has any hit: (1) exact whole-word match after normalizing case, whitespace, punctuation, quotes and hyphens; (2) speech-tolerant: number words and digits equal ("twenty" and "20"), homophones and a phonetic key; (3) fuzzy, edit distance up to one per five characters, minimum four. **Whole-word is the default**: "replace *cat* with *dog*" never touches "category". A mutating command never applies a tier-3 hit without a spoken yes, and says what it found ("I found 'recieve'. Replace it?").
- **Nearest to the caret.** Candidates are ordered by distance from the caret (ties go forward). A unique match is simply used. When there are several, the nearest is selected and shown (the selection is the highlight), and Footman says how many there are.
- **Disambiguation.** "the second one", "the last one", "the first one in the document" and "next" and "previous" step through the candidates. "The second one" counts in the announced order (nearest first); "next" and "previous" walk the document order from the current one; "first/last in the document" is explicit. "No" or "cancel" drops it. With several candidates, a mutating command selects the nearest and asks ("Replace this one? Yes, next, or the second one") rather than guessing; "replace all" says the count and asks if it is more than five (`handsfree.replace_all_confirm`).
- **Select and move.** "select *phrase*", "select this sentence / paragraph / line / word", "select from *a* to *b*", "go to the start / end of the line, paragraph or document", "move after *phrase*". Sentences are segmented by `textedit` (Unicode rules plus an abbreviation list), not by the toolkit's granularity, which differs between toolkits.
- **Replace and insert.** "replace *a* with *b*", "replace this with *b*", "delete *a*", "after *phrase* write *text*", "before *phrase* write *text*", "after this sentence write *text*" (the sentence holding the caret), "go to the end and write *text*".
- **How the edit is made.** The default is **select with `Text`, then insert with the existing ADR-0022/0029 path** (type or paste): Footman sets the selection through AT-SPI and the replacement goes in as dictation does. That keeps the app's own undo stack working (Ctrl+Z undoes it) and works in apps whose `EditableText` is read-only or does not feed the undo stack. **Direct `EditableText` edits** (`SetTextContents`, `InsertText`) are used when the typed path is not available or the field is not focusable by keys, and are per-app configurable (`handsfree.edit_mode = "select-then-type" | "direct"`). The spike (section 10) decides the defaults per toolkit with real apps. Dictated replacement text goes through the ADR-0032 formatter first; the terminal Enter rule still applies.
- **Undo.** "undo that" presses the app's undo. Footman also keeps the last edit's before and after text in memory (section 7) so "put it back" works where the app's undo does not cover it.
- **Scale and sanity.** An edit that would remove more than `handsfree.large_edit_chars` characters (default 200) or the whole content is Destructive (section 9).

### 4. Clicking by name, and the numbered overlay

**By name.** "click *Send*", "press the *Save* button", "open the *File* menu", "tick *Remember me*", "choose *Settings* tab". Footman gathers the actionable elements of the active window (roles button, link, menu and menu item, tab, check box, radio, combo, list item; showing and enabled; no password fields), scores their accessible names against the spoken name (the same tiered match as text, with the role word as a filter when spoken), and acts with `Action.DoAction` where the element has an action, or `Component.GrabFocus` for "focus the *search box*". No pointer is needed for this path, so it also works while the owner is not at the desk. If two elements tie, Footman says "Send, or Send later?" and takes "the second one", or shows numbers on just those candidates.

**Numbered overlay.** For unlabeled UI (icon buttons, canvas apps, badly built pages): "show numbers" draws a small number badge over each actionable element of the active window, and "click 7" (or "7") acts on that element. Details:
- **Drawn by Footman** as a click-through layer-shell overlay (KWin supports `wlr-layer-shell`; the repository already has a pure-Go Wayland client for the clipboard). It cannot take focus or input. It carries numbers only, never names or text. It hides after 15 s, on "hide numbers", or when the window changes.
- **Numbers are bound to element identity.** Before acting, Footman checks that the element still exists, still shows, and still has the bounds it had when numbered; if not, it says "the screen changed" and renumbers.
- **The click is `Action` first.** If the element has no action, Footman clicks at its centre with a **pointer through the RemoteDesktop portal**. That needs the pointer device type on the portal session, which ADR-0029 made keyboard-only; adding it changes the permission the owner granted and is an explicit step in setup (owner decision, below). Pointer clicks also need correct window coordinates (section 2).
- **Capped.** At most 99 numbers (`handsfree.max_numbers`), nearest to the focus first, scrolled-out elements left off ("show numbers" again after "scroll down").
- Numbers are also the disambiguation for "click *Send*" when it is ambiguous.

### 5. Scrolling, windows and apps, key chords

- **Scrolling.** "scroll down / up / to the top / to the bottom", "page down", "scroll *the list* down". `Component.ScrollTo` and `Text` range scrolling where the element supports them; otherwise PageUp, PageDown, Home, End, Space by key; a pointer axis event over the portal only if the pointer device is granted. The common cases (browser, editor, document) work by key with no pointer.
- **Windows and apps** go through the existing `platform.Windows`, backed by **KWin scripting over D-Bus** on KDE (the mechanism already in `internal/platform/linux/windows.go`), behind the Platform interface so other desktops add their own. New: "switch to *Brave*" (fuzzy by app name or title, launching the app if none is open), "next / previous window", "minimize, maximize, full screen", "close this window", "show windows" (list by voice), "go to desktop 2" and "move this to desktop 2". Closing a window is Act: apps ask about unsaved work themselves. Force-quit and kill are Destructive and belong to a later system tool. The existing `Launcher` already opens apps, sites and files.
- **Key chords.** "press control T", "press alt tab", "press escape", "press control shift left" through the existing RemoteDesktop keyboard session (ADR-0029): a small chord parser takes modifier words (control, shift, alt, super), key names (letters, digits, F-keys, arrows, home, end, page up/down, tab, enter, escape, delete, backspace, space) and "then" for sequences. **Terminals:** following ADR-0032, a spoken "press enter" is **not** sent to a terminal on its own; it asks first (Destructive-equivalent, voice yes allowed), because Enter runs the command. A deny list of chords never sent by voice (for example Ctrl+Alt+Delete, Ctrl+Alt+F-keys, Super combos that open Footman's own launcher) is configurable. ADR-0045's key navigation for dictation ("go to the end", word and line movement while dictating) is that ADR's; this layer owns the explicit, spoken `press` command and shares the chord parser.
- **Follow-ups without the wake word.** "show numbers" then "click 7", or "replace this one?" then "next", should not need "Alfred" in front each time. A command that opens a context (numbers, a candidate list, a question) opens a **follow-up window** (10 s, configurable) in which only that context's replies are accepted without the wake word: a number, "next", "previous", "the second one", "yes", "no", "cancel", "hide numbers". Anything else needs the wake word. A sticky "control mode" that accepts any command without the wake word is **not** part of this ADR (it weakens the played-back-audio protection in design §29); see the open questions.

### 6. Which apps expose AT-SPI, and enabling it

What is known, and what must be verified on the owner's real session (the spike, section 10, exists because agents cannot probe the live desktop and these claims are from toolkit documentation and general experience, not measured here):

| App family | AT-SPI quality | Notes |
|---|---|---|
| GTK 3 and 4 (GNOME apps, Gedit-style editors, Firefox's GTK shell) | Good | `GtkEntry`, `GtkTextView` and buttons implement `Text`, `EditableText`, `Action`. GTK 4 has its own AT-SPI backend. Honors `IsEnabled`. |
| Qt Widgets and KDE apps (Kate, Dolphin, KWrite, KMail, Konsole, Okular) | Good, with gaps | Qt builds a bridge when the bus enables accessibility, or with `QT_LINUX_ACCESSIBILITY_ALWAYS_ON=1`. Kate's editor view implements `Text` and `EditableText`. **Konsole**: its terminal view's text exposure must be verified; treat as read-only text, never as an editable field. |
| Qt Quick and Kirigami | Mixed | Depends on whether the app sets `Accessible` roles and names. The numbered overlay is the fallback. |
| LibreOffice | Good for Writer and Calc, one paragraph per accessible | Uses the GTK or Qt plugin's bridge; the virtual buffer joins paragraphs. Verify which VCL plugin the owner's install uses. |
| Chromium and Brave | Good **once enabled** | The page tree is built only when Chromium sees an accessibility client or is forced. Reliable route: `--force-renderer-accessibility` (a flags file or a `.desktop` override that setup offers to write and records in the install receipt), or enabling it in `brave://accessibility`. Costs some page-load time. Text offsets in rich content need checking (Chromium counts UTF-16 internally). |
| Electron (VS Code, Discord, Slack, Obsidian) | Same as Chromium | Needs the same switch; Electron apps usually follow the bus's enabled flag but some need the flag. The Monaco editor in VS Code has its own screen-reader mode that may need turning on. |
| Firefox | Good once the bus enables it | Verify content process behaviour. |
| Terminals (VTE-based, Konsole) | Read at best | Footman never edits terminal text through accessibility; key navigation only. |
| Flatpak and Snap apps | Usually fine | Both give sandboxed apps access to the accessibility bus; verify for any app the owner uses. |
| Games, canvas and custom-drawn UI, remote-desktop views | None | Fallbacks (section 7); the numbered overlay has nothing to number. |

**`footman a11y check`** (a later work item) reports, for the focused app, which interfaces it exposes and what Footman can do there ("Brave: text and buttons; enable accessibility to read page text"). It is also the owner's diagnostic when "it can't see the text" in some app.

### 7. Fallbacks when AT-SPI is missing or partial

A ladder, tried in order for each operation, with Footman saying which rung it used when that matters to the result:

1. **App recipes.** A small data file of per-app key recipes that use the app's own features: Find (Ctrl+F) and Find and Replace (Ctrl+H or Ctrl+R) dialogs, "go to line". Brave and Chromium leave a found phrase selected when Find is closed with Escape, which lets "replace" work on a page's editable area. Recipes are data, not code, so the owner can add an app.
2. **Clipboard round trip.** Select all, copy, read the clipboard through the data-control client (ADR-0029 saves and restores the previous clipboard), do the search in `textedit`, then for plain-text targets write back the changed text with paste. It changes the caret and scroll position and cannot be used on rich text without losing formatting, so it is limited to apps marked plain text (editors, terminals' command lines are excluded) or allowed per app.
3. **Footman's own record.** Dictation keeps an in-memory record of what it typed in the current focus (ADR-0045 owns the record, this layer reads it): the text, order and which window. It supports "replace *that*", "scratch that" and "put it back" for what Footman just typed, using Backspace counts (`InputEditing.Backspace`, ADR-0032). It is invalidated on any focus or window change or after a quiet period, since Footman cannot see what the owner did with the mouse or keyboard in between, and it says so when it refuses.
4. **Primary selection.** On Wayland, the PRIMARY selection holds what is selected in most apps without any copy; Footman can read it (same `selection` rules as section 9) for "what is selected".
5. **Key navigation only.** "go to the end", "select the last three words", "select this line" by keystroke (ADR-0045). No find, no replace; Footman says "I can't see the text in this app, but I can move around by keys."

No rung ever works around the password rule in section 9.

### 8. Windows and macOS (later, same interface)

- **Windows (UI Automation).** The `IUIAutomation` COM API provides what `Accessibility` needs: `TextPattern` and `TextRange` (including `FindText`, `Select`, `GetText`, move by unit), `ValuePattern` (set text), `InvokePattern` (click), `SelectionItemPattern`, `ScrollPattern`, `IsPassword` for password fields, and the focused element directly. Chromium and Electron expose a page tree once a UI Automation client attaches, or with `--force-renderer-accessibility`. Called from Go through `syscall` or `go-ole` (decided when it is built). Part of Sprint 10 (issue #24).
- **macOS (AXUIElement).** `AXFocusedUIElement`, `AXValue`, `AXSelectedTextRange`, `AXSelectedText`, `AXPress`, `AXSecureTextField` for passwords. It needs the **Accessibility permission** in Privacy and Security and, for Electron and Chrome, `AXManualAccessibility` set on the app. Reached through cgo or a small Objective-C bridge. Also Sprint 10.
- Both map onto `platform.Accessibility` unchanged; this is why the interface speaks in elements, capabilities and rune offsets, and why `textedit` and `handsfree` are OS-neutral. CI cross-compiles the interface and fake for both from the start (design §16).

### 9. Safety

Reading screen text is the most sensitive thing Footman does. The rules, in order of importance:

1. **Passwords are never read.** An element with the password role (AT-SPI `ROLE_PASSWORD_TEXT`, UI Automation `IsPassword`, macOS `AXSecureTextField`), or whose accessible name looks like a secret (password, passcode, PIN, secret, token, API key, card number, CVV, security code; matched whole-word, configurable), is skipped by every operation: not read, not searched, not numbered with its content, not edited, not copied, and not typed into by dictation (Footman says "that's a password field"). A deny list of apps (default: KeePassXC, Bitwarden, 1Password, KWalletManager, Seahorse) is treated as a sensitive window, with the same effect. A terminal's non-echoing password prompt shows nothing and is not special; terminal content is read-only and sensitive (below).
2. **Local deterministic commands read locally.** "find", "replace", "go to", "click *name*" read what they need, act, and keep nothing. The text and names are used in memory for that one command and dropped. They never reach the AI, the action log, the event bus, history or the local API. That is why those commands are permission level Read or Act and need no context grant.
3. **The AI sees a new context item, `screen`.** It means "text and element names read from the focused window through the accessibility layer". Like `window_title`, `clipboard` and `selection` it is **off by default** for both `context.local` and `context.cloud`, is covered by `all`, and follows ADR-0039: dropping it is a tightening that works by voice at once, adding it is a loosening that needs a click. Without it, the AI-facing tools return only structure ("a field in Kate, 3 matches") and not content. The local and cloud lists are separate, as for the other items: the owner may allow the local model to see the screen and not the cloud one. Element names are screen text and are gated by `screen` too; window titles stay under `window_title`.
4. **Bounded and redacted for the AI.** Tools return snippets: each match with at most ±80 characters of context, at most 20 matches, at most 4,000 characters in a result, and elements as role and name only, at most 60. Text runs through the same secret redaction as the action log (ADR-0038). Terminal content is never returned to the AI through this layer, whatever `screen` says; reading the terminal is a job for the future terminal plugin.
5. **Everything read from the screen is untrusted.** Every result carrying screen text sets `Result.Untrusted` (ADR-0015, ADR-0021). A web page can say "click Send" in its own text.
6. **UI actions are gated by what they can cause, and by taint.** This amends ADR-0015, which only gates *outward* and *destructive* calls once untrusted content is in context. Pressing keys, clicking and replacing text are ordinary Act calls, but they can send mail (click Send), submit a form or delete a file. So a tool carries a new `ToolSpec.Drives` marker, and **once untrusted content is in the task's context, every tool that drives the screen is treated as Outward by the gate**, with a confirmation showing exactly what will be pressed, clicked or typed and where. A new utterance starts a clean task, so a spoken "click Send" is not tainted by the page the AI read a minute ago; a page-driven "click Send" inside an AI task is. Deterministic voice commands from the owner are not AI tasks and are not tainted.
7. **Permission levels** (design §14, §29; `permissions.*` as usual):

   | Operation | Level | Notes |
   |---|---|---|
   | Focus info (app, role, field name), list windows, "what window is this" | Read | No content. |
   | Read, find, select, caret move; show numbers; hide numbers; scroll | Read for reading, Act for the rest | Local only. Reading for the AI also needs the `screen` item. |
   | Switch, focus, minimize, maximize, close window; switch desktop | Act | Close is left to the app's save prompts. |
   | Press a chord | Act | Enter alone in a terminal, and denied chords, are confirmed or refused as in section 5. |
   | Click by name or number | Act | **Outward** when the label looks like send, submit, post, publish, pay, buy, order, confirm purchase, share; **Destructive** when it looks like delete, remove, erase, empty trash, uninstall, format, discard, reset (editable list, `handsfree.risky_labels`; unrecognized labels in the owner's language are Act). |
   | Replace, insert, delete text | Act | **Destructive** over `handsfree.large_edit_chars` (default 200) or the whole content, or in a tainted task. |
   | Tools the AI calls to drive the screen | as above, and Outward when tainted | Section 9.6. |

   With the defaults (`destructive` and `outward` set to `confirm`), "click Send" asks once ("Click Send in Thunderbird?", a voice yes is allowed per ADR-0038). The owner can loosen it with a click per level, and later per app (#89).
8. **Logging.** The action log records the operation, app, role, counts and the user's own spoken request, never document text, field contents or element names beyond the spoken target. Redaction (ADR-0038) applies as for any tool.
9. **No new door for others.** The layer is not exposed on the local API in this change and plugins do not get it. A plugin that needs to read the screen will need its own manifest permission (for example `screen`), approved by a click at install; that is for the plugin host to design (ADR-0005).
10. **Break-glass stop** (ADR-0016, ADR-0017) clears the overlay, drops any pending candidate list and follow-up window, and cancels an edit in progress.
11. **Visible state.** While the overlay or the follow-up window is open, the tray shows it (design §29), and Footman always says what it selected or changed in one short line, so the owner can undo.

### 10. Testing and what needs the owner's desktop

- **Agents can do without a desktop:** `textedit` (pure), the grammar, tool wiring against `platform/fake`, and the AT-SPI binding against a **fake accessibility bus**: a private `dbus-daemon` with a scripted `org.a11y.atspi` service, the way the existing portal tests do (`testbus_test.go`). Window control can use the nested KWin harness (`internal/platform/linux/nested.go`). Nothing in these touches the real session bus.
- **Only the owner can do:** anything that needs the live AT-SPI bus and real apps (Kate, Konsole, Brave, LibreOffice, VS Code): the probe spike, the final check of each backend feature, enabling Brave's accessibility, and the acceptance run. Those work items are labelled `needs-human`. Agents must not probe the live accessibility bus.
- A spike comes first (work item S4-01): a read-only probe the owner runs once, which prints each app's roles, interfaces and text lengths and **never the text**, and measures offsets, `GetExtents` under Wayland and the effect of `IsEnabled` and the Chromium flag.

## Work items
Epic #111 and the plan in [`docs/sprints/sprint-04.md`](../sprints/sprint-04.md); the base-versus-plugin table is design §34.

## Alternatives considered
- **Screenshots plus a vision model ("computer use").** Works everywhere, but sends the whole screen to a cloud model (a far bigger privacy cost than bounded snippets), is slow and costly per step, and cannot place a caret or replace a phrase exactly. It stays a possible later fallback for the apps in the "none" row of section 6, off by default and behind the `screen` item.
- **Chrome DevTools Protocol on the owner's own browser.** Needs a remote-debugging port on the owner's real profile, which lets any local process drive the browser and read every logged-in session. Rejected for the owner's browser; CDP stays for Footman's own isolated browser profile (design §7) as a plugin.
- **`xdotool`, `ydotool`, evdev or uinput for everything.** Blind: it can type but cannot read, find or verify. It is what ADR-0022 already ranks as a fallback only.
- **KWin scripting for text.** KWin knows windows, not their content. It stays the window layer.
- **Riding on Orca (the screen reader).** Orca is an AT-SPI client for speech output and is not designed to be driven by another program; going to AT-SPI directly is smaller.
- **One tool per app in plugins.** It would give excellent per-app behaviour but needs a plugin for each app before basic editing works. App recipes (section 7) give the same reach for the cases that matter as data.

## Consequences
- The owner can edit and drive the apps they already use by voice, with no mouse, on KDE Wayland first.
- Footman becomes a screen reader's peer: it holds a session-wide accessibility switch and an AT-SPI connection while hands-free is on. That is a real new capability and the reason for the strict section 9. A bug here is a privacy bug, so the permission and redaction work lands before the AI gets any of these tools.
- Quality varies by app; Brave needs one setup step, terminals stay key-only, and the fallbacks are weaker than the main path. `footman a11y check` and a visible "I used the clipboard" note keep that honest.
- `plugins.ToolSpec` gains `Drives` and the gate learns the tainted rule (a contract change). `context.*` gains `screen` (a config schema change). `platform.Accessibility` and `InputKeys` (chords) are new optional interfaces.
- The text-aware seam in ADR-0045 is implemented by this layer. Names and signatures are to be reconciled when that ADR merges.
- The RemoteDesktop session may gain the pointer device later, which re-asks the permission once (owner decision).
- Windows and macOS port the backend only.

## Open questions (owner decisions)
1. **Pointer permission.** Add the pointer device to the RemoteDesktop session so the overlay and pointer-only elements can be clicked? It re-prompts once, and shows KDE's remote-control indicator. Without it, "click 7" works only for elements with an `Action`.
2. **Footman flips `IsEnabled`** while hands-free is on, and setup may write a Brave/Chromium flag. Fine, or confirm each time?
3. **Dictating into password fields.** This ADR refuses (the owner says the password aloud in a room with a live microphone). Confirm.
4. **Click on risky labels asks by default** (Outward and Destructive follow `confirm`). Loosen "Send" by default, or keep?
5. **Control mode.** Allow an opt-in sticky mode with no wake word? It is convenient and weakens the played-back-audio defence; this ADR does not include it.
6. **Sprint numbering.** Hands-free takes the name "Sprint 4"; the original Sprint 4 (assistant and jobs) and later sprints would move down by one. See `docs/sprints/sprint-04.md`.
