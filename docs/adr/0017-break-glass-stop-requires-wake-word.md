# 0017. Break-glass stop requires the wake word

- Status: Accepted
- Date: 2026-11-24
- Supersedes: 0016

## Context
ADR-0016 made the stop phrase work without the wake word. The owner pointed out that "stop everything" is something people say to *other people*, for example on a Teams call, and that must not cancel their work.

## Decision
The break-glass stop phrase **requires the wake word** ("Alfred, stop everything" / "Alfred, cancel everything"). It is still handled first, ahead of the router, the AI, and confirmations, so it always works and responds instantly. The hotkey and tray icon remain wake-word-free ways to stop.

## Consequences
No accidental stops during conversations. Hands-free stopping needs the wake word; the hotkey is the instant path.
