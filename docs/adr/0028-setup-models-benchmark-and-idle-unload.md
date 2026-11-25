# 0028. Setup fetches models and benchmarks; speech models unload when idle

- Status: Accepted
- Date: 2026-11-25
- Amends: 0010, 0024, 0027

## Context
ADR-0027 left three follow-ups for S1-07: `footman setup` does not yet fetch models, warm up the GPU or benchmark; the VAD's silence window is not a config key; and `speech.unload_after` is in the schema but does nothing. Without the first, the first dictation after install downloads 570 MB or compiles Vulkan shaders (about 29 s) in the middle of a sentence. Without the third, the 1–2 GB of speech models stay in memory all day, and there is no way to switch local AI off.

## Decision
**Setup (`internal/speech/speechsetup`, wired in `cmd/footman/setup_speech.go`).** After the config and database exist and before the service is installed, setup:
1. picks the engine with `accel.Choose` from the config and the detected hardware, exactly as the daemon will, and fetches its model and the VAD model with `models.Fetch`. A model that is already present is not touched, so setup works offline when the models are there. Each model and the models folder go on the install receipt before the download starts, and the receipt is saved at once (ADR-0023).
2. if the choice is a GPU, builds the engine through the registry (the load is the warm-up, so the shader compile happens here and its cache is recorded in the receipt), then transcribes 5 seconds of synthetic voice through the `speech.STT` interface: one untimed run, then timed runs until 5 s have passed (at most 5), and takes the median.
3. unless the user fixed `speech.stt.accelerator`, does the same for the CPU row of the ADR-0024 table (fetching its model) and compares. If the GPU path is slower, or fails to load or run, it writes `speech.stt.accelerator = "cpu"` with a comment saying why and how to undo it; the daemon's next start then lands on the CPU row. If the CPU path fails, the GPU is kept. A user-fixed accelerator is warmed up but never compared or changed.
4. prints each step as it happens (download progress on stderr).

`--skip-models` skips the whole step (fetch, warm-up, benchmark) and prints how to fetch later. A failed download fails setup, before the service is installed, with a message naming `footman models fetch` and `--skip-models`; setup is safe to re-run. A build without native engines prints a note and skips the step. The step is injected into `install.Setup` (`SetupOptions.Speech`) so the install package stays free of speech code, and the GPU/CPU decision is tested with fake engines and a fake clock.

The benchmark compares the paths the daemon would actually run (turbo on Vulkan against Parakeet or base.en on the CPU), on a synthetic clip. These engines' run time depends on the length of the audio, not on what is said. A weak iGPU such as the 2-CU Raphael is the case it exists for (ADR-0024's pending results).

**`speech.vad.min_silence`** (float, seconds, default 0.5, 0.1–5, restart to apply) is passed to Silero's `min_silence` option. The wake-word spike's 0.25 s splits dictation at short pauses, so the default stays 0.5.

**`speech.unload_after`** (duration, default `10m`, `0s` = never; already in the schema) now works (`internal/speech/idle`). The daemon builds each engine at start as before (so a missing model still stops it), then wraps speech-to-text, the VAD and text-to-speech in a `Holder`. A caller holds the engine while it uses it (a `Transcribe` call, an open `Detect` stream, an unclosed audio stream); when nobody has held it for `unload_after`, the Holder closes it, and the next use builds a fresh one with the factory and options the engine was first built with, then waits for the load. The setting is read when the engine goes idle and again when the timer fires, so changing it takes effect without a restart (lowering it applies from the next idle period).
- The VAD runs for as long as the microphone is open, so it stays loaded while Footman is listening; it unloads when listening is off. Speech-to-text and text-to-speech are what is freed in daily use.
- A reload costs the model load: about 0.6 s for whisper turbo on Vulkan with a warm shader cache, about 4.5 s for Parakeet on the CPU. That is the price of the idle memory; `0s` avoids it.
- `status.get` gains `models: {unload_after, models: [{name, engine, loaded, since, loads, in_use}]}`, and `footman status` prints a `models:` line such as `stt unloaded 4m0s ago, vad loaded (unload after 10m0s idle)`.

## Consequences
- The first dictation after setup is fast, and a weak iGPU no longer makes dictation slower than the CPU would.
- `speech.unload_after = "1s"` is the local-AI off switch for memory-tight machines; `0s` turns unloading off.
- Re-running setup after a hardware change re-measures; deleting the written `accelerator` line lets the choice be made again.
- The ZenBook and Intel Arc results (#7) are not in yet; when they arrive, ADR-0024's table may change, and the benchmark already protects against a wrong default.
- Windows and macOS keep their own packaging follow-ups.
