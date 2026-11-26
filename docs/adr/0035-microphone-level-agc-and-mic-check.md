# 0035. Microphone level: automatic gain and `footman mic check`

- Status: Accepted
- Date: 2026-11-26
- Amends: 0033

## Context
The live wake-word test (#8) found input level the main risk: quiet microphones give weak detections, and users have no way to see why. Issue #64.

## Decision
- **A level normaliser, `audio.AGC`, in pure Go.** It runs on captured frames before the VAD and wake-word engine, in both the always-listening loop and push-to-talk. It aims for -20 dBFS RMS, moves its gain slowly (attack 0.3 s, release 2 s), caps the gain (`audio.agc_max_gain_db`, default 24), holds the gain on frames below -50 dBFS so silence and room noise are not pumped up, and lowers the gain at once when a peak would pass 0.98, so output never clips. It works in place without allocating; the pipeline stage copies each frame so the platform's buffers are untouched. `audio.agc` is on by default.
- **`footman mic check`** lists input sources, listens to one for 5 s (nothing is written to disk), draws a live meter with peak and RMS of the raw signal, warns when the level is too low (RMS below -40 dBFS or peak below -20) or silent, and points to the echo-cancel source and the input volume. Interactively it can try other sources and save one to `input.microphone` with the comment-preserving editor. `footman setup` offers it at the end. The existing key `input.microphone` is used rather than a new `audio.mic`.
- **`audio.prefer_echo_cancel`** (default off): when `input.microphone` is `default` and an input source whose name contains "echo-cancel" exists, the daemon uses it.

## Consequences
Quiet microphones work better with no setup, at the cost of amplified noise up to the cap while someone speaks. The check measures the raw signal, so it shows what the system delivers, not what Footman does with it. Tests use synthetic signals and the fake platform; no test opens a microphone.
