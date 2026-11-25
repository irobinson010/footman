# 0025. Local API v1 wire details and daemon wiring

- Status: Accepted
- Date: 2026-11-25
- Amends: 0018

## Context
ADR-0018 and design §30 fix the local API's shape (JSON-RPC 2.0, Unix socket for the CLI, events as notifications, a public `v1`) but leave the details a client needs open: how messages are framed on the socket, how events are subscribed to and look on the wire, which error codes mean what, and where the socket is when the client has no platform loaded. Building `footman daemon` (S1-01) also raised questions the design doesn't answer: what the daemon does on a machine with no real platform or speech engine yet, and how far a socket client is trusted.

## Decision
- **Framing.** On the Unix socket each message is one line of UTF-8 JSON ending in `\n` (newline-delimited JSON); JSON never contains a raw newline, so no length prefix is needed. A message may be a request, a notification or a JSON-RPC batch array. Lines over 1 MiB are a parse error and close the connection. Standard library only.
- **Concurrency.** Requests on one connection run concurrently and responses may arrive out of order; clients match them by `id`. A slow `say.text` never holds up `status.get`.
- **Events.** `events.subscribe {topics}` returns a subscription ID; events then arrive as `event` notifications `{subscription, topic, seq, at, data}`. Topics are bus topic names (`pipeline.state`), a prefix pattern (`pipeline.*`) or `*`; none means all. A subscription ends with `events.unsubscribe` or the connection. Each subscription has a bounded queue; a client that falls behind loses events rather than slowing the pipeline, and sees the gap in `seq`.
- **Event payloads are API types,** separate from the core's Go types: snake_case fields and string enums (`{"from":"idle","to":"routing","turn":1}`), so the public contract doesn't move when the core is refactored.
- **Error codes.** The JSON-RPC standard codes, plus `-32000` failed, `-32001` busy (another utterance is being handled), `-32002` not found, `-32003` unavailable. Unknown members in `params` are an invalid-params error, so typos don't pass silently.
- **Methods in this first cut:** `status.get`, `events.subscribe`/`unsubscribe`, `say.text {text, as}` (`as` = `command` by default, `dictate`, or `wake`, which makes "stop everything" the break-glass stop as ADR-0017 requires), `jobs.list`/`cancel`, `config.get`/`set`/`schema`, `system.stop_everything`. The reference is `docs/api/v1.md`.
- **Socket location.** `$XDG_RUNTIME_DIR/footman.sock` on Linux, as design §10 says, with the socket 0600; `/tmp/footman-<uid>/footman.sock` (the directory created 0700) where there is no runtime directory, matching the Linux platform's `Paths().Runtime()`. The daemon refuses a socket directory that another user owns or can enter. `--socket` or `$FOOTMAN_SOCKET` override it. The CLI and the daemon compute this the same way, in `cmd/footman`, so a client finds the daemon without loading a platform; `Platform.Paths()` implementations must agree with it. A live socket is never taken over; a stale one is replaced.
- **Socket trust.** A connection on the socket is the owner at their own keyboard, with the same trust as editing `config.toml` by hand: `config.set` may change security-sensitive keys. The physical-click rule of §29 is for changes that arrive by voice or from the AI, which never reach the socket directly.
- **Daemon wiring.** For each area the daemon uses the implementation the config names (`speech.stt.engine`, `speech.tts.engine`, `ai.provider`, `jobs.worker`; the platform by OS; VAD and wake word as `sherpa` until they get config keys) if it is registered. If it isn't, the area is left out when it is optional (no VAD means no microphone; no TTS means replies as notifications), and the daemon refuses to start when the platform or speech-to-text is missing. **`--dev-fakes`** lets every area fall back to its `fake` implementation instead; the fakes are linked into the binary for this, the daemon prints a warning, and `status.get` reports `dev_fakes: true`. It is for development and tests only.
- **Shutdown.** SIGTERM or SIGINT stops accepting connections, cancels requests in flight, stops the microphone loop, cancels jobs, then closes the bus and the store; the socket file is removed.

## Consequences
- Any language can talk to the daemon with a socket and a JSON library (`socat` works for poking at it).
- The CLI (`status`, `say`, `stop`, `events`) is a thin client over the same API third parties use.
- Until real engines land, the daemon only runs with `--dev-fakes`; that is deliberate, so a release build never silently runs on fakes.
- Follow-ups: the WebSocket transport (Sprint 5) reuses `api.Server` and the methods; unmuting after a stop has no API method yet; pipeline settings are read at start, so `config.set` on a live key takes effect for the API at once but for the pipeline on restart, until the pipeline supports live updates; engine options from config are passed to factories once real engines exist.
