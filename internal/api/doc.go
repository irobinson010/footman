// Package api is the daemon's local API (design §30, ADR-0018, ADR-0024):
// JSON-RPC 2.0 over a Unix socket, one JSON value per line. The HTTP and
// WebSocket transport for the web UI comes later and will reuse the same
// Server and methods.
//
// The layers are:
//
//   - Server and Client: transport-neutral JSON-RPC 2.0 with newline-delimited
//     framing, requests handled concurrently, server-to-client notifications.
//   - Listen: the Unix socket, created with user-only permissions (§29).
//   - Methods: the v1 methods (status.*, events.*, say.*, jobs.*, config.*,
//     system.*) bound to a core.Pipeline, the event bus and the config file.
//
// The method names, parameters and results are the public v1 API, documented
// in docs/api/v1.md and experimental until Footman 1.0.
package api
