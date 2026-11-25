# 0018. Local API: JSON-RPC over socket and WebSocket, public and versioned

- Status: Accepted
- Date: 2026-11-24

## Context
The CLI, the web UI, the phone (later), and third-party clients all need to talk to the daemon, with live status and, later, audio from remote mics.

## Decision
- **JSON-RPC 2.0** for every client, the same message style plugins use (ADR-0005).
- **Transports:** the Unix socket (named pipe on Windows) for the CLI; a **WebSocket** for the web UI, the phone, and other clients.
- **Live events** are JSON-RPC notifications pushed over the same connection.
- **Client audio:** the API is designed now so clients can stream mic audio to the core (binary WebSocket frames tied to a session). This will be built with the phone phase.
- **Public and versioned:** documented as `v1`, marked **experimental until Footman 1.0**, so others can build clients (Stream Deck, launchers, desktop extensions).

## Consequences
One protocol shape across the project. API changes after 1.0 need versioning discipline.
