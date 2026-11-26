# 0032. Dictation commands and code mode

- Status: Accepted
- Date: 2026-11-26

## Context
In the first push-to-talk tests "new line" was typed as words, and "ps space dash aux" came out as `PS space dash A U X`. Whisper also adds its own punctuation and capitals around spoken commands ("New paragraph."). Terminals make it worse: Enter runs a command, so a stray "new line" there is dangerous (ADR-0015).

## Decision
`dictation.Formatter` (`internal/dictation/format.go`) formats a transcript before it is typed. It is deterministic, has no AI, and keeps its phrase lists per language in `lang.go` (`Langs`, keyed by `user.language`; unknown codes use English).

**Matching.** Phrases match case-insensitively on whole words, ignoring the punctuation around each word, longest phrase first. A command's own trailing punctuation is dropped. Spoken punctuation replaces the punctuation Whisper put on the word before it ("Hello, period" is `Hello.`).

**Text mode.** "new line" is Enter and "new paragraph" is two. Spoken punctuation: period, comma, question mark, exclamation mark, colon, semicolon, open/close quote, open/close paren. "all caps … end caps" uppercases the words between. "scratch that" removes the current chunk of the utterance (back to the last sentence end or command). If the utterance has nothing earlier, it takes back the previous insertion by sending BackSpace once per character, through the optional `platform.InputEditing` interface. "literal" before a phrase types the phrase's words instead.

**Code mode.** On when the focused app is a terminal (`InputEditing.FocusedTerminal`, which reuses the Linux terminal list used for paste), or after "command mode" / "code mode". "normal mode" / "text mode" leave it, and a spoken switch beats the focused app until the next one. In code mode there are no auto-capitals and no trailing period, and fillers are dropped but repeated words are kept ("dash dash"). Words are lower case. Spoken symbols become characters (dash, double dash, dot, slash, backslash, pipe, tilde, underscore, equals, plus, star, colon, at, hash, dollar, ampersand, greater/less than, quote, single quote, comma, semicolon, parens). A symbol joins to its neighbours. Two plain words are separated by a space. Single letters and digits join each other ("a u x" is `aux`, "A.U.X." too). "space" is an explicit space. "cap x" is one capital letter.

**Terminal safety.** Footman never sends Enter, or any line break, while a terminal has focus. "new line" and "new paragraph" are ignored there, in either mode, and the final text is stripped of line breaks as a last guard. The user runs the command. (Shift+Enter is not used.)

**Config.** `dictation.commands` (existing; `off` turns the formatter off outside code mode) and the new `dictation.terminal_mode` (default true; false stops the automatic switch to code mode, but the Enter rule still holds).

## Consequences
- Without `InputEditing` (other platforms, until they implement it) text is formatted as in a non-terminal and "scratch that" with nothing earlier in the utterance does nothing.
- "scratch that" takes back only the last insertion and only once; it cannot know what the user typed or moved since.
- Saying "period" in text mode always types a full stop; the escape is "literal period".
- A spoken code mode is sticky until changed.
