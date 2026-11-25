# 0026. Linux audio: pactl, parec and pacat subprocesses

- Status: Accepted
- Date: 2026-11-25
- Amends: 0003

## Context
`platform.Audio` on Linux must list devices, capture 16 kHz mono float32 frames with under 50 ms from producer to frame (ADR-0021), play TTS streams, and ideally report device changes. Kubuntu runs PipeWire with `pipewire-pulse`, which speaks the PulseAudio protocol; other distributions run PulseAudio itself. Options:
- **`parec`/`pacat`/`pactl` subprocesses** (package `pulseaudio-utils`, present on both servers).
- **Pure-Go PulseAudio native-protocol client.** No process, but a few thousand lines to write or a young third-party dependency, and the protocol has version quirks.
- **`pw-cat`.** PipeWire only, and no JSON device listing.
- **cgo miniaudio or libpulse.** Breaks the pure-Go build and the cross-compile jobs.

## Decision
Use `pactl`, `parec` and `pacat`. Everything in `internal/platform/linux/audio*.go`, pure Go, no new Go dependency.
- **Format and resampling.** The server converts: `parec --raw --format=float32le --rate=16000 --channels=1`. No resampler in Footman.
- **Latency.** `--latency-msec=20` on both tools; the Go side reads whatever has arrived and sends it at once (no fixed frame size, no buffering on playback). Measured on PipeWire 1.6 with the default 1024-sample quantum, a click handed to `Play` shows up in a `Capture` frame of the sink's monitor in 25 to 35 ms (median), which covers both directions, so capture alone is well under 50 ms. The cost of a subprocess is one pipe hop (microseconds).
- **Devices.** `pactl --format=json list sources|sinks` plus `get-default-source|sink`. Monitor sources are not listed as microphones but `Capture` accepts them by name. An unknown device is `platform.ErrNotFound`.
- **Device changes.** `pactl subscribe` is parsed into `DeviceChange` events (new or removed source or sink, default changed). This is a Linux-only method, `Audio.Watch`, not part of `platform.Audio`; consumers re-list on an event. A capture whose device disappears has its channel closed, as if unplugged, and the caller reopens.
- **Mic status (design §29).** `Audio.Capturing()` counts open microphones, so the daemon can set the tray to `MicOff` exactly when it is zero. The Notifier's tray is a separate task.
- **Shutdown.** The tool gets SIGTERM when the context ends, SIGKILL a second later; the channel closes once the process is reaped.
- **Missing tools** give `platform.ErrUnsupported` with an install hint.

## Consequences
- One short-lived process per capture and per `Play`; startup is tens of milliseconds, which is fine for push-to-talk and TTS but not for per-word playback.
- `Capture` cannot report a failure that happens after the process starts (for example the server dying); the channel simply closes.
- If a process per stream becomes a problem, the pure-Go native-protocol client can replace this behind the same types without touching callers.
- Tests create virtual null sinks and never touch a real microphone or speaker.
