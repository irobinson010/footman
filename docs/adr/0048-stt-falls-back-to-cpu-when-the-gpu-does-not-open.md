# 0048. Speech-to-text falls back to the CPU when the GPU does not open

- Status: Accepted
- Date: 2026-11-28
- Amends: 0010, 0024

## Context
ADR-0010 and ADR-0024 pick the GPU engine (whisper.cpp on Vulkan) whenever the hardware has one. Opening it can still fail at run time: another process may hold nearly all the GPU's memory (`ErrorOutOfDeviceMemory` while whisper.cpp allocates its decoder), or the driver may be broken. The daemon then exited, and the systemd user service restarted it in a loop until the GPU was free, so there was no dictation at all while a perfectly good CPU engine (Parakeet) sat unused.

## Decision
- **`speech.stt.accelerator = "auto"` (the default) falls back.** If the engine `accel.Choose` picked for a GPU cannot be opened, the daemon logs one `WARNING:` line (the engine, the accelerator, the first line of the error, and the CPU choice it is using) and builds what `accel.Choose` returns for accelerator `cpu` with the same engine, model and language settings. On a machine with 8 GB of RAM or more and English, that is sherpa-parakeet. A model that was named for the GPU engine is dropped when the fallback is a different engine, so the CPU engine picks its own.
- **An explicit accelerator does not fall back.** With `gpu`, `vulkan` or `cuda` the daemon still exits, and its error now says to set `speech.stt.accelerator = "cpu"` (or `"auto"`).
- **If the CPU engine fails too**, the daemon exits with both errors.
- **The path taken is visible.** The startup log shows the first choice, then the warning when it fell back. `status.get` has a new optional `stt_path` field (engine, model, accelerator, reason, and `fell back from ...`), and `footman status` prints it as `stt:`.
- **No retry.** The daemon does not try the GPU again until it restarts; the idle unload (ADR-0028) reloads the engine it fell back to. Re-trying the GPU later is left for a follow-up if wanted.

## Consequences
- A full GPU no longer takes dictation down; it costs the CPU engine's latency (about 470 ms for Parakeet, ADR-0024) until the daemon is restarted with the GPU free. The warning and `footman status` say that this happened, so it is not silent.
- A user who sets `accelerator = "gpu"` accepts that failure is fatal, as before.
- `status.get` gains a field (additive, so the API stays v1).
