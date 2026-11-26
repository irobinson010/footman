# 0034. Always-on listening loop: command capture, pause for push-to-talk, quiet mode

- Status: Accepted
- Date: 2026-11-26
- Amends: 0030, 0033

## Context
Issue #67 finishes the wake-word path of the pipeline: what happens between the wake word and the transcript, how it shares the microphone with push-to-talk, and how a user turns it down.

## Decision
- **The command is cut from the VAD segment that follows the wake word.** The spotter reports the end of the wake word (`WakeEvent.At`). The command audio starts 250 ms before it (the spotter reports late, and the first word may already have begun) and is capped at `MaxCommand` (1 min). A segment that ended before the wake word, or has less than `MinCommand` (250 ms in the daemon) left after it, is not a command: the wake stays open for the next segment. A wake that nothing follows within `WakeWindow` (8 s), or before the stream ends, is dropped silently.
- **Push-to-talk and wake listening never hold the microphone together.** A hotkey press closes the wake capture; when the recording ends the loop opens it again. The tray shows "listening for the wake word" except while a recording runs.
- **Quiet mode** (`replies.quiet`, `footman quiet on|off`, `system.set_listening`) skips the earcon and acknowledgement sounds and shows replies as notifications. The earcon is an optional clip (`Config.Earcon`; none by default), played while the microphone stays open, so it should be short and soft.
- **`footman listen on|off`** (`system.set_listening`) closes and reopens the wake microphone without restarting the daemon; push-to-talk keeps working.
- **The router strips the spotter's own wake words** (listening personas, "hey footman") plus "footman", so "Ashford, open YouTube" loses "Ashford". This closes the gap noted in ADR-0033.
- Break-glass is unchanged (ADR-0017, ADR-0033): it stops jobs and speech whether or not an utterance is being handled.

## Consequences
A wake word said alone no longer eats the next unrelated utterance, and a false wake does not leave the pipeline armed. The quiet and listen toggles last until the daemon restarts; `replies.quiet` sets the start-up value.
