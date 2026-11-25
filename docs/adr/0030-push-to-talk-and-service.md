# 0030. Push-to-talk, the KDE hotkey path and the systemd user service

- Status: Accepted
- Date: 2026-11-25
- Amends: 0022, 0025, 0028, 0029

## Context
S1-08 wires the real pieces together: `footman setup` on a real machine, then the systemd user service runs the daemon, and holding F8 records, transcribes and types. Until now the hotkeys only armed the pipeline on press and the VAD ended every utterance ("push-to-talk release is not used yet"), the microphone was open the whole time the daemon ran, and the Linux `Service` was a stub. Building the end-to-end test in a nested KWin session (`cmd/footman/e2e_test.go`) also turned up four problems with the Linux input of ADR-0029:
- KDE's GlobalShortcuts portal shows its "Global Shortcuts Requested" dialog for **every new session**, even when every shortcut is already bound. The daemon opens a new session at each start, so the user would see the dialog at each login.
- Right after setup writes the `.desktop` file, the portal's host registry can still answer "App info not found". The error was ignored, so the portal fell back to the caller's systemd scope: run from Konsole, setup bound Footman's shortcuts **under Konsole's app id**.
- KDE's portal can report the suggested key as the trigger while leaving the shortcut without a key (when another app holds it), so setup reported "bound" for a key that was not.
- The fields of KGlobalAccel's `globalShortcutsByKey` reply were read in the wrong order, so "already used by" errors named the wrong things.

The measurements also showed that unloading a model (ADR-0028) did not lower the daemon's memory: glibc kept the freed pages.

## Decision
**Push-to-talk (`internal/core/ptt.go`).**
- The dictation and command keys are push-to-talk. `input.dictate.mode` and `input.command.mode` choose `hold` (the default) or `toggle` (tap to talk).
- **Hold:** the press opens the microphone and starts a recording. The release ends the utterance after a 100 ms tail, because the last syllable is often still being spoken as the key comes up.
- **Tap:** a tap starts the recording. The VAD's end of speech, or a second tap, ends it.
- A recording is capped at 2 minutes. Presses and releases of other keys, and key repeats, are ignored while one runs.
- The microphone is open **only while a key records**, unless a wake-word engine is configured, in which case the wake-word loop keeps its own stream as before. `status.get` reports `microphone.state` as `push_to_talk` (closed until a key is pressed) or `recording`.
- The recorded audio goes through the VAD as it arrives. When the recording ends, it is trimmed to the speech the VAD found, from 0.5 s before the first speech to 0.5 s after the last. A recording with no speech is not transcribed (published as a `no speech heard` failure), so speech-to-text never invents text from silence.
- The trimmed clip is transcribed, cleaned and inserted as before. An utterance recorded while the previous one is still being handled waits for it, for up to 30 s, rather than being dropped.
- A push-to-talk press is the user asking to be heard, so it lifts a mute left by a stop. The stop discards a recording in progress.
- Speech-to-text is **held from the press until the utterance is transcribed** (`idle.STT.Hold`). An unloaded model then loads while the user speaks, and a short `speech.unload_after` cannot unload it mid-utterance.
- `pipeline.inserted` gains `latency_ms` (key up to text inserted) and `insert_ms`, and `pipeline.transcript` gains `audio_ms` and `stt_ms`. `status.get` gains `hotkeys` (key, mode, and why one could not be registered) and `last_dictation` (when, how many characters, the method and the latency, never the text). The daemon logs each turn's timings, never its text.
- The keys are registered together. If that fails, each key is registered on its own, so a command key another app holds does not stop dictation.

**Linux hotkeys and identity (amends ADR-0029).**
- **On KDE the daemon registers its keys with KGlobalAccel directly**, under the same component and ids the portal uses. It keeps the key KGlobalAccel has stored for each shortcut, which is the one setup bound or the user moved in System Settings. A shortcut with no stored key gets the configured one, if no other component holds it. The portal is still how setup binds keys (its dialog names Footman, once). On other desktops the daemon uses the portal.
- `Registry.Register` is retried for up to 3 s. If the portal still refuses the app id, Input fails with a clear error instead of acting under another app's id. A portal without the registry (before 1.19) is still accepted.
- `BindHotkeys` on KDE checks the key KGlobalAccel stored, not the portal's description. It reports a Super/Meta binding as the combination asked for.
- `globalShortcutsByKey` is read in its wire order: action, component, context.
- `platform.InputSetup` gains `PrepareInsert`. Setup calls it, so the RemoteDesktop permission dialog appears during setup rather than in the middle of the first dictation. The daemon calls it at start, so the first dictation does not wait for the session.

**Setup.** Setup binds the dictation key and then the command key, each on its own, every time it runs (re-running rebinds). It gets the insert permission, fetches and warms up the models (ADR-0028), then installs **and starts** the service.

**Service (`internal/platform/linux/service.go`).**
- The unit is `$XDG_CONFIG_HOME/systemd/user/footman.service`. Its `ExecStart` is the running binary (symlinks resolved) with the `daemon` argument.
- It has `PartOf=` and `After=graphical-session.target`, and `WantedBy=graphical-session.target`, so the daemon gets the session's Wayland display, bus and audio from the user manager.
- It sets `Restart=on-failure` and `Environment=MALLOC_ARENA_MAX=2`.
- `Install` writes the unit and runs `systemctl --user daemon-reload`, `enable` and `restart`, so a re-run picks up a new binary. `Uninstall` runs `disable --now`, removes the unit and reloads, and is a no-op without the unit.
- It is tested with a fake `systemctl`.

**Memory.** After an idle unload, the daemon calls glibc's `malloc_trim(0)` (`internal/speech/memtrim`, native builds only) and Go's `debug.FreeOSMemory`.

**Testing.** `TestE2EPushToTalk` (build tags `integration native`) runs the real binary in a nested KWin session with a private bus and XDG folders, a temporary HOME, and a `footman_test_*` null sink whose monitor is the configured microphone. It covers setup (`--no-service`), the daemon, an F8 press and release sent through the session's RemoteDesktop portal while a LibriSpeech clip plays into the sink, the text in a Qt field, idle memory, and `uninstall --purge` leaving nothing behind.

## Consequences
Measured on the workstation (Ryzen 9 7900X, RTX 4080) in the nested session. The 10 s figures are for a 10.0 s LibriSpeech utterance, from F8 up to the last character in the field:

| Speech-to-text | Release to text, 10 s (mean, min–max of 5) | Transcribe | Type 150 characters | Idle RSS, models unloaded |
|---|---|---|---|---|
| whisper.cpp turbo-q5, Vulkan | 422 ms (417–425) | ~165 ms | ~155 ms | ~255 MB |
| sherpa-onnx Parakeet, CPU | 676 ms (657–701) | ~390 ms | ~175 ms | 57–78 MB |

- Both are within Sprint 1's target of about 1 s. Typing at the 1,000 characters/s cap is a large share of it.
- Idle memory misses the < 50 MB target. On the CPU path the daemon keeps about 25–40 MB of private memory plus about 26 MB of shared code (onnxruntime, the binary). On Vulkan the GPU driver (libLLVM for the AMD iGPU's driver, the NVIDIA libraries) stays loaded after the model is freed. The follow-up is to run the GPU engine in a child process that exits when the model unloads.
- `toggle` mode ends at the first pause longer than `speech.vad.min_silence`.
- The microphone opens about 50–100 ms after the press (`pactl` checks the device, then `parec` starts). Speech in that window is lost; people rarely start talking that fast.
- If another app holds the command key (default Super+Shift+Space), setup and `footman status` say so, and dictation is unaffected.
- KDE's own shortcut list still names Footman after uninstall (ADR-0029).
- The owner's check on the real desktop (Konsole, Brave, VS Code) confirms the nested results.
