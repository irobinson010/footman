# 0005. Plugin protocol: MCP plus Footman extensions

- Status: Accepted
- Date: 2026-11-24

## Context
We want plugins in any language, plus the existing MCP ecosystem, plus Footman-specific abilities (confirmations, jobs, events).

## Decision
Plugins are separate processes speaking **MCP (JSON-RPC over stdio)**. A Footman-native plugin announces `experimental.footman.version` in its handshake and gains the `footman/*` extension methods. Each plugin has a `footman.toml` manifest declaring its permissions and config. Plugins are installed by URL; an index repo comes later.

## Consequences
One plugin client to build. Any MCP server works unchanged. Annotations from MCP servers are hints, so permissions are ultimately set by the manifest, the user, and the sandbox.
