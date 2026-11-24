# Footman: Design

Status: **draft**. This document records the design decisions made so far. Sections marked *TBD* are still open.

## 1. What it is

Footman is an open-source voice layer for your computer. It has two jobs:

1. **Dictation.** Speak, and clean text appears at your cursor. Filler words ("um", "uh", false starts) are removed. Optional speech coaching tracks your habits and suggests exercises.
2. **Assistant.** Say the wake word ("Alfred, …") and Footman acts: it opens things, answers questions, and runs background jobs such as editing code in a project or replying to a message, while you keep typing.

Each user picks a **persona**, which sets the wake word, voice, personality, and how Footman addresses them ("Yes, Master Ian").

## 2. Principles

1. **Small core, everything else is a plugin.** The core listens, understands, decides, acts, and replies. Every integration is a plugin.
2. **Every major part can be swapped.** The OS layer, speech engines, AI providers, workers, and plugins all sit behind interfaces and are chosen in config.
3. **Small and fast.** One Go binary that idles at about 20 MB of RAM. Models load on first use and unload when idle. Common commands skip the AI entirely.
4. **Safe defaults.** It is reachable only from the local machine, audio stays local, and anything destructive or outward-facing needs confirmation. Opening it up to a phone or the network is opt-in.
5. **Easy to install, easy to remove.** One command installs it, and one command removes all of it (see §11).
6. **Borrow small, build focused.** Small, best-in-class dependencies are fine. Heavy frameworks are not.

## 3. Decisions so far

| Topic | Decision |
|---|---|
| Language | **Go** for the product. Python only in the offline voice-training kit. |
| OS | **Linux first** (Wayland/KDE; X11 where needed). Windows and macOS later, through the platform interface. |
| AI providers | **Claude** (primary), **OpenAI**, **Gemini**, **Ollama**. All four have official Go SDKs. |
| Worker agents | Built-in tool loop, **Claude Code** (`claude -p`), **Codex** (`codex exec`). |
| Speech | `sherpa-onnx` (cgo) for speech detection, wake word, speech-to-text, and text-to-speech, plus cloud engines. Chosen per engine in config. |
| Speech placement | **Inside the service** for now, behind interfaces, so engines can move into their own process later. |
| Plugins | Separate processes. The manifest declares `protocol = "mcp"` or `"footman"`, and both adapt to one `Tool` interface. |
| State | `config.toml` for settings, OS keychain for secrets, folders for plugins, personas, and models, **SQLite** for history, jobs, stats, and the action log. |
| Packaging | **One `footman` binary.** No container for the core. |
| License | **Apache-2.0**. |
| Repo | **Public from day one** (free unlimited GitHub runners), under the user's GitHub org (*org name TBD*). |
| Web UI | Plain HTML, CSS, and JS, embedded in the binary. No framework or build step. |
| Built-in tools | Launcher, settings, system info, and timers are compiled in; everything else is a plugin. |
| Personas | The repo ships **original** personas. Users build their own (Alfred, Jarvis, …) locally with the provided tools. See §8. |

## 4. Process model

```
footman (single binary)
├─ footman daemon    per-user background service (systemd user unit on Linux)
│   ├─ pipeline + event bus
│   ├─ job manager
│   ├─ plugin host (mcp | footman adapters)
│   ├─ speech registry   (vad · wake · stt · tts)
│   ├─ llm registry      (claude · openai · gemini · ollama)
│   ├─ worker registry   (builtin · claude-code · codex)
│   ├─ platform          (linux now; windows / darwin later)
│   └─ local API         (Unix socket for the CLI; HTTP + WebSocket for the web UI and phone)
└─ CLI   setup · status · say · jobs · plugin · persona · config · uninstall
```

- It runs **per user**, not system-wide, because hotkeys, typing at the cursor, the mic, and the keychain all belong to the user's desktop session.
- The **web UI** (settings, history, live status, and the phone client) is embedded in the binary and served by the service.
- **Plugins start lazily** on first use and stop when idle.

## 5. Pipeline

```
idle ─wake word / hotkey─► listening ─end of speech─► transcribing ─► routing
routing ─dictation─► clean up ─► insert at cursor ─► idle
routing ─command──► acknowledgement clip ─► fast path │ AI + tools ─► reply ─► idle
                                                       └─ long task ─► job manager ─► idle (immediately)
```

- **Event bus.** Every state change becomes an event. The web UI, phone, logs, and action log all subscribe to it.
- **Fast path.** Simple commands ("open YouTube", "volume up") are matched locally without the AI. Target response time is under 100 ms.
- **Acknowledgement clips.** The persona's pre-generated audio plays instantly while the AI works.
- **AI cost control.** The system prompt and tool definitions are cached between calls, and only the tools relevant to a request are sent.

## 6. Core interfaces (sketch)

```go
// platform/ — one implementation per OS (build tags: _linux.go, _windows.go, _darwin.go)
type Platform interface {
    Input() Input          // hotkeys, push-to-talk, type/paste at cursor
    Windows() Windows      // active app, list/focus windows
    Launcher() Launcher    // open URL / app / file
    Notifier() Notifier    // notifications, tray
    Clipboard() Clipboard
    Audio() Audio          // mic / speaker devices
    Service() Service      // autostart install/remove
    Secrets() Secrets      // OS keychain
    Paths() Paths          // config / data / cache / runtime dirs
}

// speech/ — registered by name, chosen in config
type VAD interface      { Detect(ctx, <-chan Frame) <-chan Segment }
type WakeWord interface { Listen(ctx, <-chan Frame) <-chan WakeEvent }
type STT interface      { Transcribe(ctx, Audio) (Transcript, error) }
type TTS interface      { Speak(ctx, text string, v Voice) (AudioStream, error) }

// llm/
type Provider interface {
    Chat(ctx, Request) (Stream, error)   // messages + tools, streamed
    Capabilities() Caps                  // tools, vision, caching, …
}

// tools/
type Tool interface {
    Spec() ToolSpec        // name, description, JSON schema, Permission
    Run(ctx, args json.RawMessage) (Result, error)
}
type Permission int        // Read | Act | Destructive | Outward

// jobs/
type Worker interface { Start(ctx, Task) (Job, error) }
```

## 7. Acting without stealing the cursor: targets

Only dictation uses the keyboard. Everything else acts through a **target** that doesn't touch your focus:

| Target | Mechanism |
|---|---|
| Focused cursor | Type or paste into the active window (dictation only). |
| Code / project | A worker agent edits files directly. |
| Browser | Its own browser profile, driven through the Chrome DevTools Protocol. |
| Terminal | tmux or the terminal's remote-control API. |
| Apps / services | Their APIs (calendar, Microsoft Graph, …). |

Long tasks become **jobs**. Each job has a status, can be cancelled, sends a notification when done, and is stored in SQLite so it survives a restart.

## 8. Personas

A persona pack is a folder:

```
personas/ashford/
  persona.toml      # name, wake words, address form ("Master {name}"), style settings
  voice.onnx        # TTS voice (~60 MB)
  style.md          # personality prompt for spoken/chat replies
  lines.toml        # acknowledgement / quip lines → pre-generated clips
```

- The persona styles **spoken and chat replies only**. It never touches dictated text, code, or messages sent to other people.
- **Legal boundary.** The public repo ships only original personas, with voices designed from a description, not to sound like a particular actor. Character names (Alfred Pennyworth, Jarvis, Woodhouse) and voices made to imitate real actors are trademark and right-of-publicity risks, and making a voice from a prompt doesn't remove that risk. Users can build such packs for **private local use** with the provided tools, and the repo won't distribute them.
- **Voice creation tooling** produces packs. Planned sources: the ElevenLabs API, the Troupe / Resonance Lab voice pipeline, and the Python training kit.

## 9. Dictation and coaching

- Filler and false-start removal runs locally as a fast cleanup pass, with an optional AI polish.
- Opt-in **speech coaching**, off by default: fillers per minute, speaking pace, and hedge words, tracked over time in SQLite, plus suggested exercises.

## 10. State and files

| What | Where |
|---|---|
| Settings, enabled plugins, chosen persona and providers | `<config>/config.toml` (hand-editable, reloaded on change) |
| API keys and tokens | OS keychain |
| Plugins, personas, models | `<data>/{plugins,personas,models}/` |
| History, jobs, coaching stats, action log | `<data>/footman.db` (SQLite, pure Go, no server) |
| Pre-generated clips and other derived files | `<cache>/` |
| Socket | `<runtime>/footman.sock` |
| Install receipt | `<data>/install-receipt.json` |

On Linux these follow the XDG folders: `~/.config/footman`, `~/.local/share/footman`, `~/.cache/footman`, and `$XDG_RUNTIME_DIR`. Other OSes get their native locations through `Platform.Paths()`.

Privacy: transcript storage can be turned off, and retention is configurable (for example, delete after 30 days).

## 11. Install and uninstall

**Install:** download the binary or run the install script, then `footman setup`. Setup detects the hardware, downloads models (with checksums), asks for a provider and persona, stores keys in the keychain, and registers the autostart service.

**Install receipt.** Every file, folder, service unit, keychain entry, and model Footman creates is recorded in `install-receipt.json`. Plugin installs add to it. Uninstall works from this receipt, so it removes exactly what was installed and nothing else.

**`footman uninstall`** (also offered by the install script):

1. Stops the service and removes autostart.
2. Lists what will be removed, grouped and with sizes:
   ```
   Program        footman binary, service unit                    24 MB
   Models         whisper-turbo, kokoro, wake-word                1.9 GB
   Config         config.toml, enabled plugins, personas list      4 KB
   Data           history, jobs, coaching stats (footman.db)      12 MB
   Personas       ashford, alfred (yours)                         130 MB
   Secrets        3 API keys in the OS keychain
   Cache          pre-generated clips                              40 MB
   ```
3. Asks what to keep:
   - **Remove everything**: a full purge.
   - **Keep config and data**: keeps config, history, and personas, and removes the program, models, and cache. Reinstalling picks up where you left off.
   - **Keep config only.**
4. Removes keychain secrets only with an explicit confirmation.
5. Prints a summary of what was removed and the path of anything that was kept.

Non-interactive flags: `--purge`, `--keep-config`, `--keep-data`, `--dry-run`.

`footman plugin remove <name>` and `footman persona remove <name>` work the same way at a smaller scale, and each asks whether to keep that item's data.

## 12. Containers and sandboxing

- **The core runs natively, not in a container.** It needs the desktop session, mic, GPU, keychain, home folder, and the ability to launch apps. A container would have to be opened to all of these, which defeats the point, and on Windows and macOS Docker runs in a VM with no desktop access at all.
- **Optional sandboxing** for risky work: worker jobs and untrusted plugins can run under `bubblewrap` on Linux with access to only what they need (`sandbox = true` per plugin or job).
- Containers are fine for **remote components** (for example Ollama on another host) and for **reproducible builds**.

## 13. Remote and mobile access (later phase)

These are planned extras, not part of the core.

1. **Phone web app.** The phone's browser connects directly to the desktop's web UI.
2. **Native phone app.** It runs Footman on the phone for phone-side actions, and can also send commands to the desktop ("on my computer, …").

Reaching the desktop from outside the home network needs a tunnel or private network such as Cloudflare Tunnel, Twingate, or Tailscale. This is an **optional feature**, off by default.

## 14. Permissions and the action log

Both are configurable in settings:
- **Confirmation policy per permission level** (Read, Act, Destructive, Outward), with a per-plugin override.
- **Whether the action log is on**, and how long entries are kept.

## 15. Docs and optional components

The docs have separate paths for each kind of user, so no one has to read about parts they don't use:
- **Users:** install, set up, and use it.
- **Voice creators:** build persona voices. This needs the **optional** voice kit. Nothing voice-training related is installed unless asked for.
- **Plugin authors:** the plugin spec and examples.
- **Contributors:** architecture, build, and tests.

Diagrams go in `docs/diagrams/`, with PDF exports committed (tool *TBD*).

## 16. Testing

- **Fast by default.** `go test ./...` runs in seconds with no GPU, no network, no desktop, and no API keys.
- **Fakes at every interface** (Platform, STT, TTS, Provider, Worker, plugin host) let pipeline tests run end to end in memory.
- **Fixtures:** recorded AI responses, short audio clips, and golden files for text cleanup.
- **Opt-in integration tests** behind build tags (`integration`, `gpu`, `live`) for real models, a real desktop, and real APIs. They run manually or on a schedule, never by default.
- **Quality evals are separate from tests**: routing accuracy, cleanup quality, and persona tone are measured with a small eval set, not pass/fail unit tests.
- **CI:** `go vet`, a linter, unit tests on Linux, and a cross-compile check for Windows and macOS, so the platform interface stays honest before those platforms exist.

## 17. Interaction decisions

**Simple now, smart later.** Many features ship first as a simple, rule-based version (the **simple** tier) and later get an AI-assisted version (the **smart** tier) behind the same interface.

| Area | Simple | Smart (later) |
|---|---|---|
| Dictation vs. command | Wake word followed by "dictate" or "write" is dictation; anything else is a command. Separate hotkeys for each. | A small, fast AI classifies the intent. |
| Mid-dictation commands ("new paragraph", "scratch that", "all caps") | Fixed phrase list. | Understood from context. |

- **Push-to-talk:** both hold-to-talk and tap-to-start / tap-to-stop. **The wake word stays a top priority**, so everything can be done by voice alone.
- **Everything is configurable by voice.** Every setting can be changed by voice ("Alfred, turn on quiet mode"), as well as from the web UI and CLI. Footman can also explain where a setting lives and what it does.
- **Replies:** speech vs. notification-only is a setting, and there's a quiet mode.
- **False wakes:** if no clear command follows the wake word, it is dropped silently.
- **Languages:** English at launch. Spanish and Japanese are planned later, including a language-practice use case. Strings and command phrase lists are per-language from day one.

## 18. Privacy, providers, and memory

- **Offline mode** is fully supported: Ollama plus local speech. A **local-first** mode is also supported, where it stays local unless you explicitly ask it to use the cloud ("ask the cloud model…").
- **Context sharing** (window title, clipboard, selection) is off by default and enabled per item. There are two separate policies: what the **local** AI may see (can be "everything") and what **cloud** AI providers may see (a specific allow list).
- **Memory** is opt-in, and can be viewed, edited, and deleted from the web UI **and by voice** ("what do you remember about me?", "forget that").
- **Cost guardrails:** a monthly cap per provider. Footman warns as you approach it, then **asks when to remind you next**, so you don't get constant alerts.

## 19. Jobs and plugins

- Up to **2 simultaneous jobs** by default (configurable); more are queued.
- **Code-edit jobs** run on a new branch or worktree and show the diff before finishing (on by default, configurable).
- **Plugin trust:** when a plugin is installed, Footman shows the permissions it requests and asks for approval. Unknown plugins are sandboxed by default. All of this is configurable, and Footman can do it for you by voice.

## 20. Diagrams

- **Mermaid** is the in-repo source of truth. It renders natively on GitHub, so it stays the most up to date.
- **Lucidchart** produces polished versions, exported to PDF in `docs/diagrams/`, refreshed at major releases or about monthly when there are big changes.

## 21. Name

**Footman.** The binary, repo, and Go module are `footman`. Names checked on 2026-11-24: GitHub, Homebrew, crates.io, and Go module paths are free; PyPI and npm are taken by unrelated packages, which is irrelevant for a Go binary. "Majordomo" was dropped because it collides with an existing home-automation voice project. **A trademark search (USPTO, plus EUIPO if needed) is still to do before public launch.**

## 22. TTS strategy (risk area: prototype early)

The goal is a voice that sounds good, starts speaking in near real time, and needs little hardware.

1. **Distill big into small.** Offline, a large, high-quality voice source (ElevenLabs, or Troupe's S2 Pro pipeline) generates 1–3 hours of speech in the target voice. A **small, fast model** is then fine-tuned on that data, so the big model is never used at runtime. Candidates for the small model:
   - **Kokoro / StyleTTS2** (~82M parameters): the best quality for its size.
   - **Piper / VITS** (~20–60M parameters): the lightest, and it runs in real time on a CPU.
2. **Pre-generated clips** for frequent lines (acknowledgements, quips), made with the big model at full quality. They play instantly and give the best quality where it's most noticed.
3. **Streaming.** AI output is spoken sentence by sentence, so Footman starts talking before the full reply is ready.
4. **Optional cloud voice** (ElevenLabs streaming) for users who want top quality and will pay for it.
5. **A bake-off before committing**, in the spirit of Troupe Sprint 0. Measure time to first audio, how fast it generates relative to real time, RAM and GPU memory, and blind listening scores on several candidate small models.
6. **Licensing check.** Confirm the license of each voice source's outputs and model weights before training on them. Some prohibit training other models, and some model weights are non-commercial only.

## 23. Wake words

- **Early milestone:** wake-word detection built into the core (openWakeWord / sherpa-onnx keyword spotting) using **ready-made models** for a starter set of names. This comes right after dictation and is a high priority.
- **Later:** a **wake-word training kit** that lets users train a custom wake word per persona from synthetic samples (many TTS voices plus background noise). It will reuse existing tooling from the user's other repos (Troupe / Resonance Lab), so it is ported rather than built from scratch.

## 24. Repo layout and build (draft)

```
footman/
├─ cmd/footman/              single binary: CLI + `footman daemon`
├─ internal/                 everything private to the binary (free to refactor)
│  ├─ core/                  pipeline state machine, router (simple → smart tiers)
│  ├─ bus/                   event bus
│  ├─ dictation/             filler cleanup, mid-dictation commands, insertion
│  ├─ jobs/                  job manager · workers/{builtin,claudecode,codex,fake}
│  ├─ permissions/           permission gate, confirmations, action log
│  ├─ persona/               pack loading, address forms, clip cache
│  ├─ config/                TOML schema, load/validate/watch, voice-settable keys
│  ├─ store/                 SQLite + migrations
│  ├─ api/                   Unix socket + HTTP/WebSocket; embeds web/
│  ├─ install/               setup wizard, install receipt, uninstall
│  ├─ platform/              interfaces · linux/ · windows/ · darwin/ · fake/
│  ├─ speech/                interfaces · sherpa/ · cloud/{openai,elevenlabs,…} · fake/
│  ├─ llm/                   interface · claude/ · openai/ · gemini/ · ollama/ · fake/
│  └─ plugins/               plugin host (footman + mcp adapters) · builtin/ tools
├─ sdk/go/                   public: helpers for writing footman-protocol plugins
├─ web/                      web UI (embedded with go:embed)
├─ personas/ashford/         shipped original persona(s)
├─ voicekit/                 optional, Python: voice + (later) wake-word training
├─ testdata/                 audio clips, recorded LLM responses, golden files
├─ docs/  {users/, voices/, plugins/, contributing/, design.md, diagrams/}
├─ scripts/install.sh
└─ .github/workflows/
```

- **One pattern everywhere:** each swappable area is `area/` (the interface) plus one subpackage per implementation plus `fake/`. Implementations register by name, and config picks one.
- **The public surface is the protocols, not Go packages.** Plugins talk over the plugin protocol, and clients talk over the local API. Only `sdk/` is a public Go package. Everything else stays in `internal/`, so it can be refactored freely.
- **Built-in tools** (launcher, settings, system info, timers) are compiled in, behind the same `Tool` interface as external plugins.
- **Hardware acceleration is chosen at runtime, not at build time.** Each local speech engine reports the accelerators it supports. `footman setup` detects the hardware, benchmarks briefly, and picks the best option, which the user can override in config:

  | Hardware | Accelerator path |
  |---|---|
  | NVIDIA GPU | CUDA (ONNX Runtime / whisper.cpp) |
  | AMD GPU (Linux) | Vulkan (whisper.cpp); ROCm where installed |
  | Intel GPU / NPU | Vulkan; OpenVINO later |
  | Any GPU on Windows | DirectML (ONNX Runtime); Vulkan |
  | Apple Silicon (M-series, MacBook Air included) | Metal / CoreML |
  | Snapdragon / other NPUs | later (QNN), CPU arm64 meanwhile |
  | No usable GPU | CPU (AVX2 on x86, NEON on ARM), with smaller models picked automatically |

  The speech-to-text interface allows more than one engine (sherpa-onnx and whisper.cpp). whisper.cpp's **Vulkan** backend is the universal GPU path for AMD, Intel, and NVIDIA.
- **Release targets:** linux/amd64, linux/arm64, darwin/arm64, windows/amd64, windows/arm64. Public repos get free GitHub runners for Linux (x86 and ARM), Windows, and macOS, so every target is built and tested natively in CI.
- **`nocgo` build:** cloud speech only and fully pure Go. It's tiny, builds for any OS, and is a fallback for unusual platforms.
- **Releases** via GoReleaser, which builds binaries for each OS and architecture, checksums, and later the `.deb`, AUR, and Homebrew packages.

## 25. Phases (draft)

Each phase has **exit criteria**, so it's clear when it's done.

**Phase 0: Foundations and spikes**
- Repo skeleton, interfaces, fakes, config, store, bus, and CI.
- Spikes that prove the risky parts before building on them:
  1. A global hotkey and typing at the cursor on KDE Wayland.
  2. Speech-to-text latency on CPU vs. GPU, covering CUDA and Vulkan (sherpa-onnx and whisper.cpp).
  3. A start on the TTS bake-off (runs in parallel with the other work).
- *Exit:* `go test ./...` passes in under 10 s; the spikes have answered each question with numbers.

**Phase 1: Dictation MVP**
- Hold-to-talk and tap-to-talk, speech detection, speech-to-text, simple filler cleanup, simple mid-dictation commands, and typing at the cursor.
- `footman setup` and `footman uninstall` (with the install receipt), the systemd user service, and `footman status`.
- *Exit:* dictation works in a terminal, browser, and editor on KDE Wayland. Text appears within about 1 s after a 10-second utterance. Idle RAM is under 50 MB with no models loaded. Uninstall leaves nothing behind.

**Phase 2: Wake word and voice commands (simple tier)**
- Wake word with ready-made models, the simple router, fast-path commands, and settings by voice.
- The AI provider interface with Claude, a basic persona (style, address form), TTS (the bake-off winner) with the clip bank, the permission gate, and the action log.
- *Exit:* "Alfred, open YouTube" works hands-free with spoken acknowledgement in under 300 ms; false wakes are under 1 per hour of normal room noise.

**Phase 3: Assistant and jobs**
- The tool loop and job manager, with Claude Code and Codex workers.
- The plugin host (footman and MCP protocols), built-in plugins (disk report, web search), memory, cost guardrails, and the web UI.
- *Exit:* a background code edit (on a new branch, with a diff shown) runs while the user keeps typing; an MCP server and a footman-protocol plugin both work.

**Phase 4: Providers and smarts**
- OpenAI, Gemini, and Ollama providers; offline and local-first modes; the context-sharing policies.
- Speech coaching, and the smart tier (AI-based intent routing and mid-dictation commands).

**Phase 5: Reach**
- The phone web app, remote access via a tunnel, the voice kit, and the wake-word training kit.
- Spanish and Japanese; Windows and macOS ports; packages.

## 26. Open areas (next)

- [ ] Local API: endpoints, event stream format, authentication.
- [ ] Security model details.
- [ ] Plugin manifest spec (`footman` protocol details plus MCP adapter mapping).
- [ ] Persona pack spec and voice-creation workflow.
- [ ] Config schema.
- [ ] TTS bake-off plan.

