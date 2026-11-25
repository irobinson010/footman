# 0014. Web UI in plain HTML

- Status: Accepted
- Date: 2026-11-24

## Context
The web UI is mostly settings, history, and live status.

## Decision
Use **plain HTML, CSS, and JS**, embedded with `go:embed`. No framework or build step.

## Consequences
We may revisit if the UI grows significantly.
