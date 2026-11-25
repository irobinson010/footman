# 0021. Core interface shapes: shared audio types, pull streams, one generic registry

- Status: Accepted
- Date: 2026-11-25
- Amends: 0003, 0006

## Context
Design §6 sketches the core interfaces. Writing them as Go (S0-02) turned up a few points where the sketch had to be filled in or bent:

- The platform layer captures and plays audio, and the speech engines consume and produce it, so both need the same `Frame` and stream types. If `speech` defines them, `platform` has to import `speech`.
- The sketch doesn't say whether "streamed" replies (LLM chat, TTS) are channels or something else, or how a stream reports errors and is abandoned early.
- §24 says "implementations register by name, and config picks one", but not how options reach an implementation before the config package exists.
- The sketch puts `Tool` under `tools/`; the repo layout (§24) has no `tools/` and puts tools under `internal/plugins`.

## Decision
- **Shared audio package.** `internal/audio` holds `Format`, `Frame`, `Clip` and `Stream`. Samples are interleaved `float32` in [-1, 1], which sherpa-onnx and whisper.cpp take directly; speech is 16 kHz mono. `speech` keeps the design's names as aliases: `speech.Frame = audio.Frame`, `speech.Audio = audio.Clip`, `speech.AudioStream = audio.Stream`.
- **Pull streams for replies, channels for live input.** `llm.Stream` and `audio.Stream` are pull-based: `Recv()` until `io.EOF`, then `Close()` (also used to abandon a reply early). That gives each item an error, back-pressure, and no goroutine leak when the consumer stops. Continuous inputs that have no natural end (VAD, wake word, hotkeys, microphone capture, job updates) stay as channels closed when the context ends, as the sketch has them.
- **One generic registry.** `internal/registry` provides `Registry[T]` with `Register(name, Factory[T])` and `New(name, Config)`. Each area declares its own registries (`speech.STTs`, `llm.Providers`, `platform.Platforms`, `plugins.Tools`, `jobs.Workers`, …). A factory receives a `registry.Config`, a small `Decode(v any) error` interface, so the config package can hand over a TOML table later without the areas depending on it. Duplicate or empty names panic at startup.
- **`Tool` lives in `internal/plugins`**, with `ToolSpec`, `Result` and `Permission`. `Permission`'s zero value is invalid (Read = 1), so a spec that forgets its level is rejected rather than treated as read-only. `Result.Untrusted` carries the untrusted-content rule (§29, ADR-0015).
- **`Job` is an interface** returned by `Worker.Start`, with `ID`, `Status`, `Updates`, `Wait` and `Cancel`. The context passed to `Start` bounds the job's life, so the job manager passes its own, not a request's.
- **Every area has a `fake/` subpackage** registered as `"fake"`, deterministic and scriptable (queued results, recorded calls), for tests.

## Consequences
- `platform` and `speech` depend on `audio`, not on each other.
- Callers of `Chat` and `Speak` must always `Close` the stream; `llm.Collect` and `audio.ReadAll` cover the common "read it all" case.
- Options structs use `json` tags until the config package (S0-03) decides how TOML tables are decoded; it only has to implement `registry.Config`.
- The interfaces are small enough to change freely while everything is in `internal/`; later changes get a new ADR.
