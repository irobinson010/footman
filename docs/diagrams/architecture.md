# Architecture

The process model from [design §4](../design.md#4-process-model). Clients talk to one per-user daemon through the local API. The daemon's parts reach out to model providers, the desktop platform, and plugins.

*Figure: one `footman` binary runs as a per-user daemon. The CLI, web UI and phone are clients of its local API. Plugins start lazily and stop when idle.*

```mermaid
flowchart TB
    subgraph Clients
        CLI["CLI<br/>setup, status, say, jobs, plugin, persona, config, uninstall"]
        Web["Web UI<br/>settings, history, live status"]
        Phone["Phone client"]
    end

    subgraph Daemon["footman daemon (per user)"]
        API["Local API"]
        Pipeline["Pipeline and event bus"]
        Jobs["Job manager"]
        PluginHost["Plugin host<br/>mcp and footman adapters"]
        Speech["Speech registry<br/>vad, wake, stt, tts"]
        LLM["LLM registry"]
        Workers["Worker registry<br/>builtin, claude-code, codex"]
        Platform["Platform<br/>linux now, windows and darwin later"]
    end

    subgraph Providers["Model providers"]
        Claude["Claude"]
        OpenAI["OpenAI"]
        Gemini["Gemini"]
        Ollama["Ollama"]
    end

    subgraph Desktop["Desktop session"]
        Input["Mic, hotkeys, typing at cursor"]
        Keychain["Keychain"]
    end

    subgraph Plugins["Plugins (separate processes)"]
        P1["MCP plugins"]
        P2["Footman-native plugins"]
    end

    CLI -->|"Unix socket"| API
    Web -->|"HTTP and WebSocket"| API
    Phone -->|"HTTP and WebSocket"| API

    API --> Pipeline
    Pipeline --> Speech
    Pipeline --> LLM
    Pipeline --> Jobs
    Jobs --> Workers
    Pipeline --> PluginHost

    LLM --> Claude
    LLM --> OpenAI
    LLM --> Gemini
    LLM --> Ollama

    Platform --> Input
    Platform --> Keychain

    PluginHost -->|"JSON-RPC over stdio"| P1
    PluginHost -->|"JSON-RPC over stdio"| P2
```
