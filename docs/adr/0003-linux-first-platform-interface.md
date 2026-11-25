# 0003. Linux first, behind a platform interface

- Status: Accepted
- Date: 2026-11-24

## Context
The maintainer runs Linux (KDE Wayland). Friends will want Windows and macOS.

## Decision
Ship on **Linux first**. Every OS-specific action (input, windows, launcher, notifications, clipboard, audio, service, secrets, paths) goes through a `Platform` interface, with per-OS implementations selected by build tags, plus a fake for tests.

## Consequences
Porting is about adding an implementation, not refactoring. CI cross-compiles for Windows and macOS from the start so the interface stays honest.
