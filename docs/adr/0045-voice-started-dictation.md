# 0045. Voice-started dictation: sessions, stop phrases, navigation keys, notes and files

- Status: Accepted
- Date: 2026-11-27
- Amends: 0032, 0034

## Context
Dictation needed the user to hold F8. The owner wants to work fully hands-free, with the keyboard and mouse optional. The always-on loop (ADR-0034, ADR-0041) only ever transcribes a command after the wake word, and "write" or "dictate" after it was a one-shot: `Alfred write hello` typed "hello", and a bare `Alfred write` was dropped.

## Decision

**A session.** A bare start phrase after the wake word starts a dictation session that types into the focused app. The wake word is whichever of the current persona's words the router strips (Alfred, Klaus, "hey footman", `speech.wake.extra`). The start phrases are the whole command, so "write hello" stays a one-shot dictation:
- into the focused app: "write", "dictate", "take a note", "start dictation", "start dictating", "begin dictation", "begin dictating";
- into a new file: "write in a new note" (also "start a new note", "take a new note", "make a new note", "new note") makes `note <date> <time>.txt`; "write in a new document" (also "new document", "new doc") makes `document <date> <time>.md`, in `dictation.notes_dir` (default `~/Documents/Footman notes`, created if missing). The empty file is opened with the launcher (`xdg-open` on Linux), then Footman waits for focus;
- "open <file> and write" (also "…and dictate", "…and take a note", "…and type") finds the file by name.

The router sends a bare start phrase to the command route; `DictationRule` is the first fast-path rule, ahead of "open". It passes the permission gate as a tool named `dictation` (Act). With no wake-word engine there is no always-on microphone, so the phrase says so and does nothing. "stop dictation" with no session running says "I'm not taking dictation" rather than going to the AI.

**Microphone.** While a session runs, every VAD segment of the always-on microphone goes to the session, not to the wake word check. Wake words heard meanwhile are ignored (they are text), and the spotter's break-glass event still stops everything (ADR-0017). Each segment is transcribed in order, as its own turn (`pipeline.transcript` with trigger `dictate`), and typed through the same `dictate` path as push-to-talk: cleanup, the Formatter, code mode and terminal safety (ADR-0032). The speech-to-text engine is held loaded for the session. A spoken code-mode switch is reset at the start of a session.

Speech said before the start command has finished (the user did not wait for the acknowledgement) is kept in memory, up to 32 segments, while a wake-word command is being handled, and handed to the session if that command starts one; otherwise it is dropped when the command ends. It is never transcribed unless a session starts, so ADR-0034's rule that nothing is transcribed without the wake word or a hotkey holds in the sense that matters: the user said the wake word, and the audio is not sent to speech-to-text without a session.

The session's utterances are queued (64) and handled one at a time. Push-to-talk still works during a session; it pauses the wake microphone as always (ADR-0034) and the session resumes afterwards.

**Status.** `dictation.status` on the bus (`core.DictationChanged`: `active`, `phase`, `message`, `target`, `reason`) is the persistent status: "Dictating — say 'stop dictation'" while it lasts, then "Dictation stopped" with the reason. The tray shows the microphone as recording for the whole session. A desktop notification shows the same text at the start and end; the notification interface cannot keep one open or replace it, so the notification is not persistent, and the bus status, `status.get` (`dictation`) and the tray are. Nothing is spoken (the microphone is open and would hear it), except the answer to a failed start. The start of a session is marked by the normal acknowledgement clip; the end plays the persona's `done` clip from the clip bank, if there is one and quiet mode is off (`Config.StopCue`).

**Stop.** A session ends on:
- a stop phrase that is the whole utterance, ignoring case and a trailing period: `dictation.stop_phrases` (default "stop dictation", "end dictation", "that's all"; the list replaces the default and is voice-settable), "<wake word> stop" and "<wake word>" followed by any stop phrase (the wake word is matched as loosely as in ADR-0041). So "please stop dictation errors in the log" is typed;
- a stop phrase at the end of a longer utterance, but only after a sentence break (`.`, `!`, `?`, `,` or `;` on the word before): "…and that's the end. Stop dictation." types "…and that's the end." and ends. The wake-word forms ("…done. Alfred stop") work the same way. "I hope that's all" has no break and is typed;
- `dictation.idle_timeout` (default `60s`; `0s` never) with no speech;
- the break-glass stop in any form ("<wake word>, stop everything", the hotkey, the tray), which also mutes as always;
- the microphone stream ending, after the utterances already heard are typed, or wake listening being switched off.

**Navigation and editing.** A whole utterance, with an optional trailing "please" and period, that is one of these presses keys through the new optional `platform.InputKeys` (`Key(ctx, "Ctrl+End")`, implemented on Linux through the same RemoteDesktop session as typing, and by the fake platform):

| Say | Keys |
|---|---|
| "go to the end", "go to end", "go to the bottom" | Ctrl+End |
| "go to the start", "go to the beginning", "go to the top" | Ctrl+Home |
| "end of line" | End |
| "start of line", "beginning of line" | Home |
| "select all" | Ctrl+A |
| "undo", "undo that" | Ctrl+Z |
| "redo", "redo that" | Ctrl+Shift+Z |
| "press enter", "press return", "hit enter" | Enter |
| "press tab", "hit tab" | Tab |
| "press escape", "hit escape" | Escape |
| "save", "save it", "save file", "save the file", "save document" | Ctrl+S |

"new line", "new paragraph" and "scratch that" are the Formatter's (ADR-0032); "delete that" is added as another word for "scratch that". A longer sentence that contains a command ("please save the file for me") is typed. A navigation or editing command forgets the last insertion, so a later "scratch that" does not take back text that is no longer at the cursor.

**Terminal safety.** In a terminal (`InputEditing.FocusedTerminal`) the only commands that run are end of line, start of line, Tab and Escape. **Enter is never pressed in a terminal, not even by "press enter".** ADR-0032's rule has no exception ("Footman never sends Enter, or any line break, while a terminal has focus"), so this change keeps it and leaves the exception for the owner to decide. Undo, redo, save, select all and go to the start or end of the document are also not sent there, because a terminal reads Ctrl+Z as suspend and Ctrl+S as freeze, and Ctrl+A is a shell or multiplexer key. A blocked command shows a notification saying so, and is not typed. Dictated text keeps the line-break strip of ADR-0032. An Input without `InputKeys` shows a notice that the desktop cannot press keys, and the words are not typed.

**New note and file flows.** After opening the file, Footman reads the active window before the open and polls `Windows.Active` every 200 ms for up to 10 s (`Config.FocusTimeout`, `FocusPoll`) until the window changes, or its title has the file's name, then pauses 500 ms (`FocusSettle`) for the editor to take typing. A desktop that cannot say which window has focus gets a fixed 2 s pause instead. If focus never arrives the session does not start, and the user is told ("I opened it but its window never took focus… click into it and say the wake word and write"); nothing is typed into a window Footman cannot see.

"open <file> and write" uses `internal/filefind`: the desktop's recently-used list (`recently-used.xbel`) first, then a walk of the home folder that skips hidden files and folders and `node_modules`, `venv`, `site-packages`, `vendor`, `snap`, `steamapps`, `target`, `build`, `dist` and trash, stops after 2 s or 200,000 entries, and returns what it has. A name matches on the letters and digits of the file name, ignoring case, spaces, dashes and the extension ("meeting notes" finds `Meeting-Notes.txt`); ranking is exact, starts with, contains, all words; then recently used; then newest. No match says so. One match is opened. Several are read out, the top three ("I found 5 files called budget; the top three are: 1, budget.txt, in Documents. 2, …. Which one?"), and the session waits in the `choosing` phase, with no wake word, for "one", "two", "three" (also "1", "first", "the second one", "number three", with or without the wake word) or "cancel". Anything else is waited through until the idle timeout.

**Seam for text-aware editing.** `dictation.TextEditor` (`Replace`, `InsertAfter`) is the interface another design (accessibility) will implement, given to the pipeline as `Deps.Editor`. The default, `dictation.NoEditor`, returns an error wrapping `platform.ErrUnsupported`. `Pipeline.ReplaceText` and `InsertAfterText` call it for the session. No phrases are parsed yet, so "replace X with Y" is typed as text.

**API.** Topic `dictation.status`; `status.get` gains `dictation` while a session runs; the journal's `pipeline.outcome` lines for session turns have trigger `dictate` and, for commands, handlers `dictation_stop`, `dictation_edit`, `dictation_edit_blocked`, `dictation_edit_unsupported` and `dictation_pick`.

## Consequences
- Dictation works without a key. Everything in a session is typed, so a stray conversation near the microphone is typed too, until a stop phrase or the idle timeout; the status and tray say it is live. Keep the idle timeout short if that matters.
- A session needs the wake-word engine (the always-on microphone). With wake listening off or no engine it cannot start, and push-to-talk is unchanged.
- The stop phrase after a sentence break can end a session by accident when the user says "…that's all." as part of a sentence. It is in the default list because the owner asked for it; `dictation.stop_phrases` can drop it.
- The session transcribes every segment, so it costs one speech-to-text call per utterance and the transcripts go to history (`history.transcripts`) like push-to-talk's.
- Not tested live (the tests use only fakes): the Linux key presses through the RemoteDesktop session, the real `xdg-open` and window focus on KDE, and echo (the stop cue and the reply read while the microphone is open may be picked up by the microphone on a setup without echo cancellation; the cue is short, and replies in a session are only the failed-start and file-choice ones).
- Follow-ups: text-aware editing through accessibility (the seam above); a persistent notification if the notifier gains a replace or sticky mode; an exception for Enter in a terminal if the owner wants one.
