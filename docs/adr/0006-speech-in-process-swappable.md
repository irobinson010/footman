# 0006. Speech engines in-process and swappable

- Status: Accepted
- Date: 2026-11-24

## Context
Running speech engines in-process is faster and smaller; running them in a separate process isolates crashes.

## Decision
Run speech engines **in-process** for now, behind the `VAD`, `WakeWord`, `STT`, and `TTS` interfaces, registered by name and chosen in config. Moving an engine out of process later is just another adapter.

## Consequences
A crash in an engine can take down the service until we split it out.
