# 0008. No container for the core

- Status: Accepted
- Date: 2026-11-24

## Context
Containers isolate, but the core needs the desktop session, mic, GPU, keychain, home folder, and app launching. On Windows and macOS, Docker runs in a VM with no desktop access.

## Decision
Ship the core as a **native binary**. Use **bubblewrap** sandboxing for plugins and worker jobs instead. Containers are fine for remote components and builds.

## Consequences
Isolation comes from sandboxing, not containers.
