# 0040. Clip bank and acknowledgement timing

- Status: Accepted
- Date: 2026-11-26

## Context
Issue #80 (S3-03) needs a spoken acknowledgement under 300 ms. The owner's rule: measure the current flow (acknowledge after speech-to-text and routing) first, and only play the acknowledgement at the end of speech, before transcription, if the median is over 300 ms on the CPU. A wake word is checked by the second stage of #94, which transcribes the segment first, so an acknowledgement before transcription would also be played for wakes that stage rejects.

## Decision
**Bank** (`internal/persona/clipbank`). For the active persona the bank holds every line of the short kinds (`ack`, `working`, `done`, `confirm`, `error`; not quips). The daemon calls `Set(Target{Pack, Voice, Address, Lang, Salt})` at start and on every config reload; it returns at once and a background pass does the work:
1. Lines the pack ships in `clips/<lang>.<kind>.<index>.wav` (lines without `{address}` only) are used as they are.
2. Otherwise the clip is looked up on disk as `<dir>/<persona>/<hash>.wav`, where `<dir>` is the folder the daemon passes (`<cache>/clips`) and the hash covers the rendered text, persona ID and version, voice model, speaker, language, speed and `Salt`. A change to any of them makes a new name, so only the affected lines are rendered again: a new `user.name` or title re-renders only the `{address}` lines, a new voice or speed re-renders all.
3. Missing clips are rendered one at a time through `speech.TTS`, trimmed of leading and trailing silence, and written atomically. When a pass finishes with every line ready, files of that persona that no line names any more are deleted.
Rendering runs the TTS the daemon gives it; the bank never selects an accelerator, and the daemon must hand it the CPU one. A new `Set` cancels the pass in progress, and passes never overlap.

**Picking.** `Pick(kind)` and `Ack()` choose a random ready clip, never the same line twice in a row. Until an `{address}` line has been rendered with the user's address it is not a candidate, so only lines without `{address}` play and the acknowledgement is never late; and if nothing of the kind is ready, no acknowledgement plays rather than waiting. Acknowledgement clips are decoded and held in memory; the other kinds are read from disk when picked, to keep the daemon's idle memory down.

**Pipeline.** `core.Config.Ack` is a `core.AckSource` (`Ack() (audio.Clip, bool)`, which must not block); `core.FixedAck` wraps one clip. `playAck` publishes `pipeline.acknowledged` (`core.Acknowledged`) when the player first reads a sample, with `Latency` (end of speech to that sample), `Lead` (command start to that sample) and `Clip`. End of speech is the push-to-talk key release, or the moment the VAD hands over the segment for a wake-word turn (the first instant the pipeline knows speech has ended; the VAD's `min_silence`, 0.5 s by default, has already passed by then).

**Timing: acknowledge after transcription.** On the workstation (Ryzen 9 7900X, CPU only, 12 threads, median of 21 runs after warm-up), speech-to-text for command-length speech:

| Engine on CPU | 1.5 s of speech | 3 s | 5 s |
|---|---|---|---|
| sherpa-onnx Parakeet TDT 0.6B int8 | 95 ms | 138 ms | 199 ms |
| whisper.cpp base.en | 195 ms | 216 ms | 243 ms |

Routing and starting the clip take about 12 µs in the fake-platform pipeline test (`Lead`); the player adds 25 to 35 ms (ADR-0026). From the end of speech (key release, or the VAD handing over the segment) to the first sample is therefore about 130 to 240 ms with Parakeet and about 230 to 280 ms with whisper base.en, under 300 ms, so no acknowledgement is played before transcription and the second wake stage of #94 is unaffected. Ten seconds of speech (about 470 ms on Parakeet, ADR-0024) is over 300 ms, and rare for a command.

## Consequences
- Wake-word turns end at the VAD's `min_silence`, 0.5 s after the last word. Measured from the last word, a wake-word acknowledgement therefore comes about 0.65 to 0.8 s later; an acknowledgement before transcription would still not reach 300 ms from the last word, because the VAD cannot know sooner. Only a shorter `min_silence` for wake turns would, at the cost of cutting commands at pauses. This is left to the owner.
- Real TTS render times on the CPU (a full bank of about 30 lines) were not measured here; the first start with a new voice has no acknowledgement until the first clips are rendered.
- The daemon (#84) must pass the bank to `core.Config.Ack`, call `Set` on start and config reload, and record the clip folder in the install receipt's cache group. `footman persona clips [--render]` can be built on `Bank.Entries` and `Bank.Render`.
