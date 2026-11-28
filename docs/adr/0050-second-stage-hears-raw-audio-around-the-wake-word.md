# 0050. The second wake stage hears the raw audio around the wake word

- Status: Accepted
- Date: 2026-11-28
- Amends: 0041

## Context
With `speech.wake.debug` on, the owner's journal showed this for a spoken "Alfred, tell me what you use in Ollama":

    wake word heard: alfred
    turn 2: wake word not confirmed by speech-to-text (no_wake_word; 1.3 s checked in 83 ms), dropped; heard "Tell me what you use in Ollama."

The spotter (which reads the raw microphone stream) fired and the earcon played, but the audio the second stage transcribed started after "Alfred". ADR-0041 builds that audio from VAD segments only: the segment that holds the wake word, or a lone wake word kept from the segment before it. If the VAD never put the wake word into a segment, the check had nothing to hear. That happens with a short word, with a headset whose noise gate or noise cancelling clips the onset, and with a VAD that starts a segment late. The Silero VAD adds no padding of its own (a segment starts where the detector's 0.5 threshold is first crossed, on 32 ms windows, and must hold 0.25 s of speech), so the clipped start of a segment is the start of the audio the second stage gets. The earlier head logic also took any unclaimed segment that ended in the last 3 s as the wake word, so a segment of other speech could stand in for it.

## Decision
- The microphone session keeps the last 4 s of the stream it hands the spotter and the VAD in a ring buffer (`rawRing`, one allocation of about 256 KB per session, frames copied in as they pass; written before the spotter sees a frame, so it holds everything up to a wake word when that is reported).
- When a wake word is reported (with `speech.wake.verify` on), the pipeline copies the raw audio from 2 s before the end of the wake word (`wakeRawHead`) to its end, once, into the armed state.
- When the command segment is claimed and starts after that audio does, the second stage is given the raw audio, then the command. If the segment starts inside the raw audio, they join with nothing between them; if it starts later, a 300 ms gap goes between them, as for a wake word said alone. A segment that starts before the raw audio already covers it and is cut as before (up to 3 s before the wake word, `wakeLookback`).
- The VAD-derived head of ADR-0041 stays as the fallback for when there is no ring (nothing was captured with a spotter, or the ring held none of the span).
- The audio is only in memory, is dropped when the microphone session ends or the armed wake is cleared, and is never logged or written; the privacy rules of ADR-0041 and ADR-0049 are unchanged.

## Consequences
- A wake word the VAD missed or clipped no longer gets a good command dropped. The audio checked can include up to 2 s of non-speech or of speech just before the wake word, so a command said straight after other speech may still fail the check (as a merged segment already could).
- The second stage no longer takes an unrelated earlier segment for the wake word when the VAD missed it.
- The cost is a 256 KB buffer and a copy of one frame per frame (no allocation), plus one copy of at most 2 s per wake.
- The VAD's start is not changed. Raising Silero's sensitivity or padding its segments would also change dictation and follow-up capture, which is not needed now that the second stage does not depend on it.
