# Writing plugins

This section is for people who want to extend Footman with a plugin. A plugin is a separate program that Footman starts on first use and stops when idle. Plugins speak JSON-RPC 2.0 over stdio using MCP's message shapes, so plain MCP servers work unchanged, and Footman-native plugins can also use the Footman extensions (`footman/confirm`, `footman/notify`, `footman/job` and `footman/events.subscribe`).

## Coming soon

- Manifest: the `footman.toml` file in the plugin folder (id, run command, permissions and config schema)
- MCP servers: making a plain MCP server work with Footman, and how tool permissions are inferred
- Footman extensions: the handshake capability, confirmations, notifications, and background jobs
- Lifecycle: installing, lazy start, idle shutdown, and crash restart
- Examples and the SDKs: `sdk/go`, and a Python helper later on

For the draft spec, see [docs/design.md](../design.md) §26 (plugins). The plugin handshake is also drawn in [docs/diagrams/plugin-handshake.md](../diagrams/plugin-handshake.md).
