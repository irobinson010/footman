# Plugin handshake

The core ↔ plugin conversation from [design §26](../design.md#26-plugins-draft). Both sides speak JSON-RPC 2.0 over stdio with MCP's message shapes. A Footman-native plugin announces `experimental.footman` in its `initialize` result, and plain MCP servers simply don't.

*Figure: the plugin starts on first use, asks the user through the core during a tool call, and is shut down after the idle timeout.*

```mermaid
sequenceDiagram
    participant Core as Footman core
    participant Plugin as Plugin process
    participant User

    Note over Core,Plugin: Lazy start on first use
    Core->>Plugin: spawn run.command from footman.toml
    Core->>Plugin: initialize (capabilities.experimental.footman.version = 1)
    Plugin-->>Core: result (announces experimental.footman)
    Core->>Plugin: notifications/initialized
    Core->>Plugin: tools/list
    Plugin-->>Core: tools (cached at install, used for routing)

    Note over Core,Plugin: A tool call that needs confirmation
    Core->>Plugin: tools/call queue_render
    Plugin->>Core: footman/confirm "Render 3 books on Troupe?"
    Core->>User: ask by voice or in the web UI
    User-->>Core: yes
    Core-->>Plugin: footman/confirm result (approved)
    Plugin-->>Core: notifications/progress
    Plugin-->>Core: tools/call result
    Core-->>User: spoken or shown reply

    Note over Core,Plugin: Idle shutdown after idle_timeout (5m) with no calls
    Core->>Plugin: close stdin
    Core-->>Plugin: terminate if still running
```
