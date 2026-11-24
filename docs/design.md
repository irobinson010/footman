# The Butler: Design

Status: **draft**. This document records the design decisions made so far. Sections marked *TBD* are still open.

## 1. What it is

The Butler is an open-source voice layer for your computer. It has two jobs:

1. **Dictation.** Speak, and clean text appears at your cursor. Filler words ("um", "uh", false starts) are removed. Optional speech coaching tracks your habits and suggests exercises.
2. **Assistant.** Say the wake word ("Alfred, …") and the Butler acts: it opens things, answers questions, and runs background jobs such as editing code in a project or replying to a message, while you keep typing.

Each user picks a **persona**, which sets the wake word, voice, personality, and how the Butler addresses them ("Yes, Master Ian").

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
| Plugins | Separate processes. The manifest declares `protocol = "mcp"` or `"butler"`, and both adapt to one `Tool` interface. |
| State | `config.toml` for settings, OS keychain for secrets, folders for plugins, personas, and models, **SQLite** for history, jobs, stats, and the action log. |
| Packaging | **One `butler` binary.** No container for the core. |
| License | Open source (*TBD: MIT or Apache-2.0*). |
| Personas | The repo ships **original** personas. Users build their own (Alfred, Jarvis, …) locally with the provided tools. See §8. |

## 4. Process model

```
butler (single binary)
├─ butler daemon     per-user background service (systemd user unit on Linux)
│   ├─ pipeline + event bus
│   ├─ job manager
│   ├─ plugin host (mcp | butler adapters)
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
| History, jobs, coaching stats, action log | `<data>/butler.db` (SQLite, pure Go, no server) |
| Pre-generated clips and other derived files | `<cache>/` |
| Socket | `<runtime>/butler.sock` |
| Install receipt | `<data>/install-receipt.json` |

On Linux these follow the XDG folders: `~/.config/butler`, `~/.local/share/butler`, `~/.cache/butler`, and `$XDG_RUNTIME_DIR`. Other OSes get their native locations through `Platform.Paths()`.

Privacy: transcript storage can be turned off, and retention is configurable (for example, delete after 30 days).

## 11. Install and uninstall

**Install:** download the binary or run the install script, then `butler setup`. Setup detects the hardware, downloads models (with checksums), asks for a provider and persona, stores keys in the keychain, and registers the autostart service.

**Install receipt.** Every file, folder, service unit, keychain entry, and model the Butler creates is recorded in `install-receipt.json`. Plugin installs add to it. Uninstall works from this receipt, so it removes exactly what was installed and nothing else.

**`butler uninstall`** (also offered by the install script):

1. Stops the service and removes autostart.
2. Lists what will be removed, grouped and with sizes:
   ```
   Program        butler binary, service unit                     24 MB
   Models         whisper-turbo, kokoro, wake-word                1.9 GB
   Config         config.toml, enabled plugins, personas list      4 KB
   Data           history, jobs, coaching stats (butler.db)       12 MB
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

`butler plugin remove <name>` and `butler persona remove <name>` work the same way at a smaller scale, and each asks whether to keep that item's data.

## 12. Containers and sandboxing

- **The core runs natively, not in a container.** It needs the desktop session, mic, GPU, keychain, home folder, and the ability to launch apps. A container would have to be opened to all of these, which defeats the point, and on Windows and macOS Docker runs in a VM with no desktop access at all.
- **Optional sandboxing** for risky work: worker jobs and untrusted plugins can run under `bubblewrap` on Linux with access to only what they need (`sandbox = true` per plugin or job).
- Containers are fine for **remote components** (for example Ollama on another host) and for **reproducible builds**.

## 13. Open areas (next)

- [ ] Local API: endpoints, event stream format, authentication, phone pairing.
- [ ] Security model: permission levels, confirmation flow, the action log, remote access.
- [ ] Plugin manifest spec (`butler` protocol details plus MCP adapter mapping).
- [ ] Persona pack spec and voice-creation workflow.
- [ ] Config schema.
- [ ] Repo layout and build (cgo/sherpa-onnx, release matrix).
- [ ] Phases and milestones.
- [ ] License choice.
