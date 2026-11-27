# 0047. Follow-up listening and voice confirmation

- Status: Accepted
- Date: 2026-11-27
- Amends: 0015, 0038, 0039, 0044

## Context
Three gaps showed up together.

1. When Footman asks "Which city?" the owner has to say the wake word again before answering, and the AI forgets the question: each command starts a fresh conversation.
2. Click confirmations (ADR-0039) need the owner to reach for the mouse. Design §29 always allowed a spoken "yes" for outward and some other actions, and ADR-0039 left it as a follow-up.
3. ADR-0044 recorded that untrusted content (web search results) tightens Outward and Destructive actions but not Act ones (`open_url`, `open_app`, `quick_action`), so a poisoned page could still make the model open a page or start an app. It said that deserved its own decision.

Issue #120 and ADR-0046 (section 5, the 10 s follow-up window for hands-free control) want the same follow-up window.

## Decision

### 1. Untrusted content tightens Act too (amends ADR-0015, ADR-0038, ADR-0044)
Once untrusted content is in the task's context, a tool call of any level except Read needs confirmation, whatever the config says (a `deny` stays `deny`). Read tools stay automatic. `core.DefaultGate`, which has nobody to ask, refuses Act in that case as it refuses Destructive and Outward. Fast-path rules run with no untrusted content (the owner's own words), so "open YouTube" is unaffected. The untrusted flag now also survives into the follow-up of a conversation (below).

### 2. Follow-up listening
When a **spoken** reply ends with a question (its last non-space character, closing quotes and emphasis marks aside, is `?`), Footman keeps listening for the answer after the speech has ended, with no wake word. This is a **follow-up window**.
- **It is a rule about which speech counts, not a second microphone.** Wake listening already keeps the microphone and the VAD open. The window accepts a VAD segment, as `TriggerFollowUp`, when the speech began after a fence and before a deadline. Both are **stream offsets** (the frames' own clock), not wall time: the fence is the offset of the latest frame when Footman's speech ended plus a guard of 250 ms (`DefaultFollowUpGuard`), and the deadline is the fence plus `speech.followup.timeout`. Footman's own voice and its echo therefore can never be heard as the answer, and nothing is decided by timers racing playback. The window opens only after `Play` returns (the end of the reply, or of a confirmation question).
- **No wake word, no second stage.** The two-stage verify (ADR-0041) is off for these segments. A wake word said inside the window still works: it arms a normal wake command, which takes over (a confirmation window stays).
- **Same conversation.** When the AI's reply ends with a question, the pipeline keeps the conversation (messages and the untrusted flag) for the window. The answer is routed as an ordinary command, fast path first, then the AI, with that history, so "Which city?" then "Raleigh" works. A request that does not come from the window starts a new conversation; the conversation is dropped when the window closes.
- **Re-opens.** If the answer's own reply ends in a question the window opens again.
- **It closes** after `speech.followup.timeout` of silence (default `8s`, live, voice-settable; `0s` disables it); when the owner says "never mind", "nevermind", "that's all" or "cancel" (the whole utterance); when a wake command starts; when the microphone is closed (push-to-talk recording, `footman listen off`, mute); on the **break-glass stop**; and when a **dictation session** starts. Speech that began before the deadline and is still being transcribed counts.
- **Never during dictation.** A dictation session takes every segment (ADR-0045), so the window does not open while one runs, including after its own file-choice question ("Which one?"), which the session's pick handling answers.
- **Not for replies that are not spoken** (quiet mode, quiet hours, typed requests, no TTS), when `speech.followup.enabled` is off, or without the always-on microphone.
- **Visible.** The pipeline state `listening_followup` (`core.FollowUp`) is published on `pipeline.state` while a window is open, so the UI and a notification can show it. The optional listening cue is `Config.Earcon` (the clip bank has no cue kind of its own yet), played before the guard starts and never in quiet mode.
- **Config.** `speech.followup.enabled` (default `true`, live, voice-settable) and `speech.followup.timeout` (default `8s`, live, voice-settable). Both apply from the next window; changing them needs no restart.

### 3. Voice confirmation (amends ADR-0039, ADR-0038)
When the gate asks for a confirmation, the **broker's `Prompt` hook** (new, called by `Broker.Confirm` right after the request and its notification exist) lets the pipeline speak the question and listen for the answer, on the goroutine of the command that made the call:
- **The question** is built by the gate (`permissions.Question`): "Shall I open example.com?" for `open_url`, "Shall I open Firefox?" for `open_app`, "Shall I do this: volume down?" for `quick_action`, the owner's own words for a fast-path rule ("Shall I open YouTube?"), and "Shall I go ahead with send email?" otherwise. It is spoken through the turn's speaker, which now has `Sync` (speak what was fed, wait for playback to end, carry on), so it never overlaps earlier speech, and the window opens after it. The persona's generic confirm line is the fallback when there is no question.
- **Answers.** The utterance must match as a whole, apart from case and punctuation ("Yes." counts): **yes**: "yes", "yes please", "do it", "go ahead", "confirm"; **no**: "no", "cancel", "don't". "Never mind" also denies. Anything else, including "yes, send it to everyone", is not an answer and leaves the confirmation pending. The lists are `core.YesPhrases`, `NoPhrases` and `DonePhrases`.
- **The same answer works without the window.** The confirmation stays pending after the window times out (it lasts as long as the broker's timeout, 2 minutes), and "Alfred, yes" said with the wake word answers it, as does the break-glass check on that utterance.
- **Destructive actions are click-only.** The gate sets `ClickOnly` for Destructive tool calls (ADR-0038 had left it false), so `Broker.Answer(..., voice)` fails with `ErrClickOnly`. Footman says "This is a destructive action. Please confirm on screen.", opens no window and listens for nothing. Setting changes that loosen permissions stay click-only through `config.VoiceChange`, unchanged. Act and Outward actions can be approved by voice.
- **The click keeps working in parallel**; the first answer wins (the broker resolves a request once). A voice answer reaches the broker as `Answer(id, allow, "voice")`, so `confirm.resolved` carries `how: "voice"`, the gate returns it, and the action log records the decision path (`voice` or `click`) in the existing `how` column of tool and fast-path entries. The spoken words are not logged.
- A confirmation asked for a request that is not spoken is only shown.

## Consequences
- After a web search the AI's `open_url`, `open_app` and `quick_action`, and its and `set_setting`, need a confirmation (the owner can say yes). This is the intended cost of closing the Act exposure ADR-0044 described.
- **The window hears anyone in the room.** For the few seconds after a question, an unknown voice or played-back audio could say "yes". ADR-0015's protection against that is the wake word; the owner chose this trade for Act and Outward actions, and Destructive actions and security settings stay behind a click. Speaker recognition, when it lands, should gate the window.
- The window is a rule on stream offsets, so it depends on the microphone frames' `Offset`; the real Linux capture sets it and the VAD reports segments in the same clock. If the VAD's segment start is padded earlier than the speech, a very quick answer inside the guard may be missed; the owner repeats it.
- The tray still shows "listening for the wake word" during a window (a follow-up is not a push-to-talk recording). Showing it there is left to the UI work that consumes `listening_followup`.
- During a dictation session a confirmation can only be answered by click, since the session takes every segment.
- `confirm.list` and `confirm.request` are unchanged on the wire. New: `pipeline.state` value `listening_followup` and transcript trigger `followup` (docs/api/v1.md).
- Fakes: `fake.Audio.Live` (a capture that keeps taking later frames) and `HoldPlayback` (playback that does not end until released) make the timing testable without a clock or audio device.
