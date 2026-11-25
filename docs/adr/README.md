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
