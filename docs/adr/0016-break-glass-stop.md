# 0016. Break-glass stop

- Status: Superseded by 0017
- Date: 2026-11-24

## Context
There must always be a way to halt everything immediately.

## Decision
A **break-glass stop** works without the wake word: a dedicated phrase ("stop everything" / "cancel everything"), always spotted on-device, plus a hotkey and the tray icon. It cancels all jobs, stops speech, and mutes the mic.

## Consequences
False triggers are possible, but they only cancel and mute, which is the safe direction.
