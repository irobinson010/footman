# Architecture Decision Records

Each significant decision is recorded here, numbered and never deleted. A changed decision gets a new ADR that supersedes the old one. Copy [`template.md`](template.md) to add one.

| # | Decision | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-go-for-the-core.md) | Go for the core | Accepted |
| [0003](0003-linux-first-platform-interface.md) | Linux first, behind a platform interface | Accepted |
| [0004](0004-four-llm-providers.md) | Four LLM providers | Accepted |
| [0005](0005-plugin-protocol-mcp-plus-extensions.md) | Plugin protocol: MCP plus Footman extensions | Accepted |
| [0006](0006-speech-in-process-swappable.md) | Speech engines in-process and swappable | Accepted |
| [0007](0007-state-storage.md) | State storage | Accepted |
| [0008](0008-no-container-for-core.md) | No container for the core | Accepted |
| [0009](0009-apache-2-license-public-repo.md) | Apache-2.0 license and public repo | Accepted |
| [0010](0010-runtime-accelerator-selection.md) | Runtime accelerator selection | Accepted |
| [0011](0011-name-footman.md) | Name: Footman | Accepted |
| [0012](0012-original-personas-only.md) | Original personas only | Accepted |
| [0013](0013-tts-distill-big-into-small.md) | TTS: distill big voices into small models | Proposed |
| [0014](0014-web-ui-plain-html.md) | Web UI in plain HTML | Accepted |
| [0015](0015-security-confirmations-and-untrusted-content.md) | Security: confirmations and untrusted content | Accepted |
| [0016](0016-break-glass-stop.md) | Break-glass stop | Superseded by 0017 |
| [0017](0017-break-glass-stop-requires-wake-word.md) | Break-glass stop requires the wake word | Accepted |
| [0018](0018-local-api-json-rpc.md) | Local API: JSON-RPC over socket and WebSocket, public and versioned | Accepted |
| [0019](0019-voice-source-licensing.md) | Voice source licensing | Accepted |
| [0020](0020-voice-sourcing-quality-first.md) | Voice sourcing: quality first during development | Accepted |
| [0021](0021-core-interface-shapes.md) | Core interface shapes: shared audio types, pull streams, one generic registry | Accepted |
| [0022](0022-linux-input.md) | Linux input: portal hotkeys, portal typing, per-app paste | Accepted |
| [0023](0023-install-receipt-and-uninstall.md) | Install receipt and uninstall semantics | Accepted |
| [0024](0024-stt-engines-and-defaults.md) | Speech-to-text engines and defaults per hardware tier | Accepted |
| [0025](0025-local-api-v1-wire-and-daemon-wiring.md) | Local API v1 wire details and daemon wiring | Accepted |
| [0026](0026-linux-audio-pulse-tools.md) | Linux audio: pactl, parec and pacat subprocesses | Accepted |
| [0027](0027-native-speech-build-and-runtime-backends.md) | Native speech build: one binary, whisper.cpp backends loaded at run time | Accepted |
| [0028](0028-setup-models-benchmark-and-idle-unload.md) | Setup fetches models and benchmarks; speech models unload when idle | Accepted |
| [0029](0029-linux-input-implementation.md) | Linux input: app identity, hotkey binding, clipboard handling and layout check | Accepted |
| [0030](0030-push-to-talk-and-service.md) | Push-to-talk, the KDE hotkey path and the systemd user service | Accepted |
| [0031](0031-uninstall-desktop-grants-and-test-session-guard.md) | Uninstall undoes desktop grants; tests never reach the real session | Accepted |
| [0032](0032-dictation-commands-and-code-mode.md) | Dictation commands and code mode | Accepted |
| [0033](0033-wake-word-keyword-spotting.md) | Wake word: sherpa-onnx keyword spotting with a gated break-glass spotter | Accepted |
| [0034](0034-always-on-listening-loop.md) | Always-on listening loop: command capture, pause for push-to-talk, quiet mode | Accepted |
| [0035](0035-microphone-level-agc-and-mic-check.md) | Microphone level: automatic gain and `footman mic check` | Accepted |
| [0036](0036-persona-pack-format.md) | Persona pack format as built | Accepted |
| [0037](0037-interim-tts-sherpa-kokoro-piper.md) | Interim TTS: sherpa-onnx Kokoro and Piper voices, phonemized in Go | Accepted |
| [0038](0038-permission-gate-and-action-log.md) | Permission gate and action log | Accepted |
| [0039](0039-click-confirmations.md) | Click confirmations: broker, channels, expiry; tighten by voice, loosen by click | Accepted |
| [0040](0040-clip-bank-and-acknowledgement-timing.md) | Clip bank and acknowledgement timing | Accepted |
| [0041](0041-two-stage-wake-word.md) | Two-stage wake word: a sensitive spotter checked by speech-to-text | Accepted |
| [0042](0042-persona-replies-spoken-and-shown.md) | The persona talks back: wiring, live reload, replies spoken and shown | Accepted |
| [0043](0043-system-context-and-status-tool.md) | The AI knows the machine: "system" context item on by default, read-only system_status tool | Accepted |
| [0044](0044-web-answers.md) | Web answers: provider-side web search, `ai.web_search` and `user.location` | Accepted |
| [0045](0045-voice-started-dictation.md) | Voice-started dictation: sessions, stop phrases, navigation keys, notes and files | Accepted |
| [0046](0046-hands-free-accessibility-layer.md) | Hands-free control: the accessibility layer (AT-SPI2, clicks, windows, keys, safety) | Accepted |
| [0047](0047-followup-listening-and-voice-confirmation.md) | Follow-up listening and voice confirmation | Accepted |
| [0048](0048-stt-falls-back-to-cpu-when-the-gpu-does-not-open.md) | Speech-to-text falls back to the CPU when the GPU does not open | Accepted |
