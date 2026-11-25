# 0007. State storage

- Status: Accepted
- Date: 2026-11-24

## Context
We need settings users can edit and share, secure secrets, and history that grows over time.

## Decision
`config.toml` (edited in a comment-preserving way) for settings; the **OS keychain** for secrets; plain folders for plugins, personas, and models; one **SQLite** file (pure Go, no server) for history, jobs, coaching stats, memory, and the action log.

## Consequences
No database server. Config stays human-friendly.
