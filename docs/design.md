# Footman: Design

Status: **draft**. This document describes the current design. **The reasons behind each decision are recorded as ADRs in [`docs/adr/`](adr/README.md)**; when a decision changes, a new ADR supersedes the old one and this document is updated. Sections marked *TBD* are still open.

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
| Speech | `sherpa-onnx` (cgo) for speech detection, wake word, speech-to-text (Parakeet), and text-to-speech; `whisper.cpp` (cgo) for speech-to-text on GPUs and in other languages; plus cloud engines. Chosen per engine in config, or by hardware (ADR-0024, ADR-0027). |
| Speech placement | **Inside the service** for now, behind interfaces, so engines can move into their own process later. |
| Plugins | Separate processes. The manifest declares `protocol = "mcp"` or `"footman"`, and both adapt to one `Tool` interface. |
| State | `config.toml` for settings, OS keychain for secrets, folders for plugins, personas, and models, **SQLite** for history, jobs, stats, and the action log. |
| Packaging | **One `footman` binary.** No container for the core. |
| License | **Apache-2.0**. |
| Repo | **Public from day one** (free unlimited GitHub runners), at `github.com/irobinson010/footman`. |
| Web UI | Plain HTML, CSS, and JS, embedded in the binary. No framework or build step. |
| Built-in tools | Launcher, settings, system info, and timers are compiled in; everything else is a plugin. |
| Personas | The repo ships **original** personas. Users build their own (Alfred, Jarvis, …) locally with the provided tools. See §8. |
| Linux input | Setup binds hotkeys through the GlobalShortcuts portal; on KDE the daemon registers them with KGlobalAccel, because KDE's portal asks again for every new session (ADR-0030). Typing as RemoteDesktop portal keysyms; paste through a data-control clipboard plus portal-sent Ctrl+V, or Ctrl+Shift+V in terminals, with the clipboard restored afterwards (ADR-0022, ADR-0029). See §9. |
| Push-to-talk | Hold the key to talk (release ends the utterance) or tap to talk (end of speech ends it). The microphone is open only while a key records, unless the wake word is on (ADR-0030). |
| Service | A systemd user unit tied to the graphical session, installed and started by setup (ADR-0030). |

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

How the walking skeleton (S0-05, `internal/bus`, `internal/core`, `internal/dictation`) fills in the details:
- **Bus semantics.** Topics are typed and named (`pipeline.state`, `jobs.update`, …). Publishing never blocks. Each subscriber has a bounded buffer, and events that don't fit are dropped for that subscriber and counted. A bus-wide sequence number shows any gaps. The local API can subscribe to every topic at once.
- **Only addressed speech is transcribed.** The wake word arms the pipeline, and the next end of speech is transcribed. Speech that ends while nothing is armed is never sent to speech-to-text.
- **Two-stage wake word (ADR-0041).** The spotter is tuned to miss little; each wake it reports is checked by transcribing the audio from just before the wake word, and the turn goes ahead only if the transcript starts with a wake word. Rejected wakes are dropped silently (`speech.wake.verify`).
- **Push-to-talk (ADR-0030).** The dictation and command keys open the microphone for one utterance. In hold mode, releasing the key ends the utterance, after a 100 ms tail. In tap mode, the VAD's end of speech or a second tap ends it. The recording is trimmed to the speech the VAD found (silence is never transcribed). Speech-to-text is held loaded from the press to the transcript. Without a wake word, the microphone is closed the rest of the time.
- **Break-glass first.** The stop phrase is checked right after transcription and before routing, even while another utterance is being handled. Every other utterance is refused as busy until the pipeline is idle again.
- **Long tasks.** The AI hands a long task to the worker by calling a built-in `start_task` tool. Footman replies at once, the pipeline goes back to idle, and the job reports progress on the bus.
- **Interim permission gate.** Until confirmations exist, read and act tools run, and destructive and outward tools are refused, with the refusal shown to the AI.

## 6. Core interfaces

The Go definitions live in `internal/` (ADR-0021); this is the shape. Every method that may block or do I/O takes a `context.Context` first.

```go
// audio/ — PCM shared by platform and speech: float32 samples, 16 kHz mono for speech
type Frame struct { Format; Samples []float32; Offset time.Duration }
type Clip  struct { Format; Samples []float32 }
type Stream interface { Format() Format; Recv() (Frame, error); Close() error } // io.EOF at end

// platform/ — one implementation per OS (build tags: _linux.go, _windows.go, _darwin.go)
type Platform interface {
    Input() Input          // hotkeys, push-to-talk, type/paste at cursor
    Windows() Windows      // active app, list/focus windows
    Launcher() Launcher    // open URL / app / file
    Notifier() Notifier    // notifications (with click actions), tray status
    Clipboard() Clipboard
    Audio() Audio          // mic / speaker devices: Capture → <-chan Frame, Play(Stream)  (Linux: pactl/parec/pacat, ADR-0026)
    Service() Service      // autostart install/remove
    Secrets() Secrets      // OS keychain
    Paths() Paths          // config / data / cache / runtime dirs
}
// Optional extensions, found by type assertion:
//   Input as InputSetup: SetupInput(ctx, binary) installs what names Footman to the desktop
//     (a .desktop file on Linux) and lists files for the receipt; BindHotkeys(ctx, combos)
//     binds exactly those keys (setup, and rebinding).
//   Input as dictation.Layout: Typeable(text) says whether the keyboard layout can type it.

// speech/ — registered by name, chosen in config (Frame, Audio, AudioStream alias audio/)
type VAD interface      { Detect(ctx, <-chan Frame) <-chan Segment }
type WakeWord interface { Listen(ctx, <-chan Frame) <-chan WakeEvent }
type STT interface      { Transcribe(ctx, Audio) (Transcript, error) }
type TTS interface      { Speak(ctx, text string, v Voice) (AudioStream, error) }

// llm/
type Provider interface {
    Chat(ctx, Request) (Stream, error)   // messages + tools; Stream.Recv() yields text deltas, tool calls, Done
    Capabilities() Caps                  // tools, vision, caching, local, context size
}

// plugins/
type Tool interface {
    Spec() ToolSpec        // name, description, JSON schema, Permission, fast-path phrases
    Run(ctx, args json.RawMessage) (Result, error)   // Result.Untrusted marks outside content (§29)
}
type Permission int        // Read | Act | Destructive | Outward (zero value is invalid)

// jobs/
type Worker interface { Start(ctx, Task) (Job, error) }
type Job interface    { ID() string; Status() Status; Updates() <-chan Update; Wait(ctx) (Result, error); Cancel() }

// registry/ — one generic by-name registry, declared per area
var STTs = registry.New[STT]("stt")          // speech.STTs, llm.Providers, platform.Platforms, plugins.Tools, jobs.Workers, …
func init() { speech.STTs.Register("whispercpp", newWhisper) }
stt, err := speech.STTs.New(name, opts)      // opts: registry.Config, decoded into the engine's options
```

Replies (LLM chat, TTS) are **pull streams**: `Recv` until `io.EOF`, then `Close`, which also abandons them early. Continuous inputs (VAD, wake word, hotkeys, capture, job updates) are channels closed when the context ends.

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
- **Inserting text** (`dictation.insert`, ADR-0022): `auto` types short single-line text whose characters are all on the current keyboard layout and pastes everything else. On Linux the layout check reads the compositor's keymap, and typing sends the layout's own keysyms at most 1,000 characters/s through one RemoteDesktop portal session. A restore token in the data folder means the permission dialog appears only once. Paste offers the text on the clipboard, marked so clipboard history skips it, and presses the focused app's paste keys through the same session. It puts the previous clipboard back once the app has read it (ADR-0029).
- **Hotkeys** are bound with the desktop. Setup binds the key the user chose, and running setup again rebinds it. If the user later moves the shortcut in the desktop's settings, events still report the configured combination (ADR-0029). On KDE the daemon registers through KGlobalAccel with the stored key, so no dialog appears at login. A key another app holds is reported in `footman status` without stopping the other keys (ADR-0030).
- Opt-in **speech coaching**, off by default: fillers per minute, speaking pace, and hedge words, tracked over time in SQLite, plus suggested exercises.

## 10. State and files

| What | Where |
|---|---|
| Settings, enabled plugins, chosen persona and providers | `<config>/config.toml` (hand-editable, reloaded on change) |
| API keys and tokens | OS keychain |
| Plugins, personas, models | `<data>/{plugins,personas,models}/` |
| History, jobs, coaching stats, action log | `<data>/footman.db` (SQLite, pure Go, no server) |
| Pre-generated clips and other derived files | `<cache>/` |
| Socket | `<runtime>/footman.sock` (`$FOOTMAN_SOCKET` overrides it) |
| Install receipt | `<data>/install-receipt.json` |

On Linux these follow the XDG folders: `~/.config/footman`, `~/.local/share/footman`, `~/.cache/footman`, and `$XDG_RUNTIME_DIR` (or `/tmp/footman-<uid>`, created 0700, when it is unset). Other OSes get their native locations through `Platform.Paths()`. The CLI works out the socket path without loading a platform, the same way `Platform.Paths().Runtime()` does (ADR-0025).

Privacy: transcript storage can be turned off, and retention is configurable (for example, delete after 30 days).

## 11. Install and uninstall

**Install:** download the binary or run the install script, then `footman setup`. Setup detects the hardware, downloads models (with checksums), asks for a provider and persona, stores keys in the keychain, and registers the autostart service. Models are fetched with `footman models fetch` into `<data>/models`, checked against pinned SHA-256s and recorded in the receipt; setup calls it for the engine it picked, then warms up the GPU and benchmarks the GPU against the CPU, and picks the CPU (writing `speech.stt.accelerator = "cpu"` with a comment) if the GPU is slower. It works offline when the models are already there (ADR-0028).

**Desktop integration.** Where the platform's Input implements `InputSetup`, setup also installs the files that name Footman to the desktop, binds the push-to-talk keys (dictation, then commands, each on its own), and asks for permission to type and paste, so that dialog appears during setup rather than at the first dictation. On Linux the file is `io.github.irobinson010.Footman.desktop`, and the receipt also lists the RemoteDesktop restore token (ADR-0029, ADR-0030).

**Service.** On Linux this is the systemd user unit `~/.config/systemd/user/footman.service`. It runs `footman daemon` with the graphical session and restarts it if it fails. Setup installs, enables and (re)starts it as its last step. Uninstall stops, disables and removes it (ADR-0030).

**Install receipt.** Every file, folder, service unit, keychain entry, and model Footman creates is recorded in `install-receipt.json`. Plugin installs add to it. Uninstall works from this receipt, so it removes exactly what was installed and nothing else. Each entry has a kind (file, folder, tree, model, service, secret) and a group (the headings below); secret values are never recorded. Folders are removed only when empty, so files Footman did not install are left and reported (ADR-0023).

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
   - **Remove everything**: keeps nothing (keychain secrets still need step 4).
   - **Keep config and data**: keeps config, history, and personas, and removes the program, models, and cache. Reinstalling picks up where you left off.
   - **Keep config only.**
4. Removes keychain secrets only with an explicit, separate confirmation.
5. Prints a summary of what was removed and the path of anything that was kept.

Non-interactive flags: `--purge` (everything, secrets included, no questions), `--keep-config`, `--keep-data`, `--remove-secrets` (the explicit confirmation for step 4), `--dry-run` (changes nothing, service included). With no terminal and none of `--purge`, `--keep-config` or `--keep-data`, uninstall refuses to guess. When data is kept, the receipt is kept too and rewritten to list only what remains (ADR-0023).

**Setup flags:** `--name`, `--persona`, `--provider`, `--api-key-stdin` (the key is never a flag), `--hotkey` (default F8, ADR-0022), `--no-service`, `--skip-models` (no model download, warm-up or benchmark), `--non-interactive`. Run on a terminal, setup asks for whatever was not given.

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

- **Dictation formatting (simple tier, [ADR-0032](adr/0032-dictation-commands-and-code-mode.md)):** spoken "new line", "new paragraph", punctuation, "scratch that", "all caps … end caps" and a "literal" escape, from per-language phrase lists. In a terminal, or after "command mode", **code mode** turns "ps space dash aux" into `ps -aux`. Footman never presses Enter in a terminal.
- **Push-to-talk:** both hold-to-talk and tap-to-start / tap-to-stop. **The wake word stays a top priority**, so everything can be done by voice alone.
- **Everything is configurable by voice.** Every setting can be changed by voice ("Alfred, turn on quiet mode"), as well as from the web UI and CLI. Footman can also explain where a setting lives and what it does.
- **Replies:** spoken in the persona's voice **and** shown as a desktop notification by default (`replies.speak`, `replies.notify`). Quiet mode and quiet hours silence the speech but keep the notification ([ADR-0042](adr/0042-persona-replies-spoken-and-shown.md)).
- **False wakes:** if no clear command follows the wake word, it is dropped silently.
- **Languages:** English at launch. Spanish and Japanese are planned later, including a language-practice use case. Strings and command phrase lists are per-language from day one.

## 18. Privacy, providers, and memory

- **Offline mode** is fully supported: Ollama plus local speech. A **local-first** mode is also supported, where it stays local unless you explicitly ask it to use the cloud ("ask the cloud model…").
- **Context sharing** (window title, clipboard, selection) is off by default and enabled per item. The exception is **`system`**: coarse facts about the machine (OS, desktop, hardware, locale, time zone) and the current time, on by default so answers fit this computer; no names, paths or content ([ADR-0043](adr/0043-system-context-and-status-tool.md)). The AI can also look at what the machine is doing now with the read-only `system_status` tool (process names only, never command lines). There are two separate policies: what the **local** AI may see (can be "everything") and what **cloud** AI providers may see (a specific allow list).
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

**Input level (ADR-0035).** Input level is the main wake-word risk in real rooms (#8). Captured frames go through a small pure-Go level normaliser (`audio.AGC`) before the VAD and wake-word engine: target RMS -20 dBFS, slow attack (0.3 s) and release (2 s), a noise gate at -50 dBFS that holds the gain instead of raising it, a gain cap (`audio.agc_max_gain_db`, default +24) and a peak guard that never clips. `audio.agc` is on by default. `footman mic check` (also offered at the end of `footman setup`) lists input sources, listens for 5 s without storing anything, shows a live meter and peak/RMS, warns when the level is too low, and saves the chosen source as `input.microphone`. `audio.prefer_echo_cancel` (default off) picks an echo-cancelling source when the microphone is `default`.

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
│  ├─ audio/                 PCM primitives shared by platform and speech
│  ├─ registry/              generic by-name registry used by every swappable area
│  ├─ install/               setup wizard, install receipt, uninstall
│  ├─ platform/              interfaces · linux/ · windows/ · darwin/ · fake/
│  ├─ speech/                interfaces · sherpa/ · whispercpp/ · accel/ · models/ · cloud/{openai,elevenlabs,…} · fake/
│  ├─ llm/                   interface · claude/ · openai/ · gemini/ · ollama/ · fake/
│  └─ plugins/               Tool interface · plugin host (footman + mcp adapters) · builtin/ tools · fake/
├─ sdk/go/                   public: helpers for writing footman-protocol plugins
├─ web/                      web UI (embedded with go:embed)
├─ personas/ashford/         shipped original persona(s)
├─ voicekit/                 optional, Python: voice + (later) wake-word training
├─ testdata/                 audio clips, recorded LLM responses, golden files
├─ docs/  {users/, voices/, plugins/, contributing/, design.md, diagrams/}
├─ scripts/install.sh
└─ .github/workflows/
```

- **One pattern everywhere:** each swappable area is `area/` (the interface) plus one subpackage per implementation plus `fake/`. Implementations register by name in the area's `registry.Registry`, and config picks one (ADR-0021).
- **The public surface is the protocols, not Go packages.** Plugins talk over the plugin protocol, and clients talk over the local API. Only `sdk/` is a public Go package. Everything else stays in `internal/`, so it can be refactored freely.
- **Built-in tools** (launcher, settings, system info, timers) are compiled in, behind the same `Tool` interface as external plugins.
- **Hardware acceleration is chosen at runtime, not at build time.** Each local speech engine reports the accelerators it supports. The daemon detects the hardware at start and picks the best option by the ADR-0024 table (`speech.stt.engine = "auto"`); the user can override engine, model and accelerator in config. Setup runs a short benchmark of the GPU against the CPU and picks the CPU if the GPU is slower (ADR-0028). whisper.cpp is built with its GPU backends as plugins, so one binary loads CPU, Vulkan or CUDA at run time (ADR-0027):

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
- **Native and pure-Go builds:** the local speech engines need cgo and are compiled with `-tags native` (`make native`), which links whisper.cpp (built by `scripts/native/whispercpp.sh`) and sherpa-onnx's prebuilt libraries; releases ship them in `lib/footman/` beside the binary (ADR-0027). Without the tag the build is pure Go: cloud speech only, tiny, builds for any OS, and is what `go test ./...` uses.
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

## 26. Plugins (draft)

### Protocol: one wire format, MCP plus Footman extensions

Both plugin kinds speak **JSON-RPC 2.0 over stdio**, using MCP's message shapes (`initialize`, `tools/list`, `tools/call`, progress notifications). A Footman-native plugin is an MCP server that also announces the Footman extensions in its handshake:

```json
"capabilities": { "experimental": { "footman": { "version": 1 } } }
```

That handshake field is the "header" that says which kind it is. The core has **one plugin client**; plain MCP servers work unchanged and simply don't get the extensions. Plugin authors can use any MCP SDK, in any language.

**Footman extensions** (only for plugins that announce them):

| Method | Direction | Purpose |
|---|---|---|
| `footman/confirm` | plugin → core | Ask the user yes/no (by voice or UI) mid-task. |
| `footman/notify` | plugin → core | Show a notification or speak a short status line. |
| `footman/job` | plugin → core | Promote a long tool call to a background job with status and cancel. |
| `footman/events.subscribe` | plugin → core | Receive bus events (needs the `events` permission). |

### Manifest: `footman.toml` in the plugin folder

```toml
[plugin]
id = "troupe"                        # unique, lowercase
name = "Troupe"
version = "0.1.0"
description = "Queue and monitor audiobook renders on a Troupe server"
license = "Apache-2.0"
footman = ">=0.1"                    # compatible core versions

[run]
command = ["./bin/troupe-plugin"]    # or ["uvx", "some-mcp-server"], ["npx", …]
idle_timeout = "5m"
platforms = ["linux", "darwin", "windows"]

[permissions]                        # requested; the user approves at install
network = ["troupe.example.com:8443"]  # hosts, or "any"
filesystem = { read = [], write = [] }
desktop = false                      # type at cursor, read windows/clipboard
events = false                       # subscribe to the event bus
sandbox = "auto"                     # auto | always | never

[config]                             # schema → web UI form + voice-settable keys
url   = { type = "string", required = true, description = "Troupe server URL" }
token = { type = "secret", description = "API token" }   # stored in the OS keychain

[[tools]]                            # per-tool metadata (optional for MCP servers)
name = "queue_render"
permission = "act"                   # read | act | destructive | outward
phrases = ["render {book}"]          # optional simple-tier fast-path triggers
```

### Permission mapping for plain MCP tools

Each tool's permission is set in this order (the first one that applies wins):
1. A `[[tools]]` entry in the manifest.
2. The user's override.
3. The MCP tool's annotations: `readOnlyHint` → read, `destructiveHint` → destructive, `openWorldHint` → outward.
4. Otherwise **act**.

Annotations are hints from the plugin, so they can only be trusted as far as the plugin's install approval and sandbox allow.

### Lifecycle

- **Install** from a release archive URL (prebuilt binaries per platform, with sha256), a git URL, a local path, or a package-runner command (`uvx` / `npx`) for existing MCP servers. The Butler shows the requested permissions and asks for approval. Each install is recorded in the install receipt.
- **Tool metadata is cached at install**, so routing never has to start a plugin just to see what it offers.
- **Lazy start** on first use, **idle shutdown**, and a **crash restart** with backoff. A plugin that keeps crashing is disabled, with a notification.
- **Tool selection:** only the tools relevant to a request are sent to the AI (matched from plugin and tool descriptions), so many installed plugins don't bloat every prompt.
- **Data:** each plugin gets its own data folder; removing a plugin asks whether to keep that folder.

### Decided
- **Install by URL** to start. A community plugin index repo comes later.
- **Event access** is allowed, but only with the explicit `events` permission approved at install.
- **SDKs:** `sdk/go` plus a thin **Python helper** over the official MCP Python SDK (phase 3).

## 27. Persona packs (draft)

A persona pack is a folder (or a `.tar.zst` archive with a sha256), installed by URL just like plugins:

```
personas/ashford/
  persona.toml
  style.md               # personality prompt (spoken/chat replies only)
  lines.en.toml          # acknowledgement / status / quip lines, one file per language
  voice/ashford.onnx     # TTS voice for the local engine
  wake/ashford.onnx      # optional custom wake-word model
  clips/                 # optional pre-generated clips for lines without {address}
```

```toml
[persona]
id = "ashford"
name = "Ashford"
description = "A dignified British butler with a dry wit"
version = "1.0.0"
license = "CC-BY-4.0"
languages = ["en"]

[wake]
words = ["ashford", "hey ashford"]
model = "wake/ashford.onnx"      # optional; otherwise ready-made / keyword spotting
sensitivity = 0.5

[address]                        # how the persona addresses the user
form = "{title} {name}"          # → "Master Ian"
title = "Master"
fallback = "sir"                 # used until the user sets a name
# Users can override all of this in their config.

[voice]
engine = "kokoro"                # local engine for voice/…; or "piper"
model = "voice/ashford.onnx"
speed = 1.0
cloud = { provider = "elevenlabs", voice_id = "" }   # optional premium voice

[style]
prompt = "style.md"
formality = 0.8                  # 0–1
humor = 0.4                      # 0–1
verbosity = "brief"              # brief | normal | chatty
quips = "sometimes"              # never | sometimes | often (user-overridable)
```

```toml
# lines.en.toml — {address} is filled in per user
ack     = ["Right away, {address}.", "Very good, {address}."]
working = ["One moment, {address}.", "Seeing to it now."]
done    = ["It is done, {address}."]
confirm = ["Shall I proceed, {address}?"]
error   = ["I'm afraid that didn't work, {address}."]
quips   = ["I shall add it to my list of grievances."]
```

**Clips:** lines without `{address}` can ship pre-generated in the pack. Lines with `{address}` are generated locally with the pack's own voice when the user sets their name (and again if it changes), then cached.

**Safety boundaries:**
- A pack can't declare tools, permissions, or config outside its own fields.
- `style.md` is text from the internet and could carry a prompt injection, so it is length-limited and inserted as a delimited *style* section. The core rules (permissions, confirmations, never styling dictation, code, or outgoing messages) come after it and always win.
- The style applies to spoken and chat replies only (§8).

**Switching:** by voice ("switch to Ashford"), CLI, or web UI.

**Several personas at once (optional, default 1):** several personas can listen at the same time, each with its own wake word and voice. Whichever wake word you use answers.

**Languages:** a pack can include lines and a voice per language (`lines.es.toml`, `voice/es/…`). It falls back to English when a language is missing.

**Shipped personas** (all original characters):
| Persona | Character |
|---|---|
| **Ashford** | A dignified British butler with a dry wit. |
| **Klaus** | A German butler: precise, formal, efficient. |
| **Pettigrew** | An eager, slightly hapless valet (the comic one). |
| **Lucy** | A calm, precise AI assistant. |

**Klaus at launch:** speaks **English with a German accent and manner**, with an occasional German word ("Jawohl, Herr Ian"). A German-language Klaus comes with German language support later.

### Voice-creation workflow (voice kit, optional, phase 5)
1. **Source:** a description (ElevenLabs voice design), Troupe / Resonance Lab, or the user's own recordings with consent.
2. **Dataset:** generate 1–3 h of speech from a balanced script (about 1,500–3,000 sentences).
3. **Train:** fine-tune the small model chosen by the bake-off (§22).
4. **Evaluate:** blind A/B listening against the source, time to first audio, and real-time factor.
5. **Package:** generate clips for lines without `{address}`, write `persona.toml`, and run `footman persona install ./dist/<id>`.

## 28. Config (draft)

**Principles**
- **Every key has a default**, so an empty or missing `config.toml` is valid.
- **One schema, defined once in Go code.** It drives validation, the web UI settings form, the generated docs, and the list of settings that can be changed by voice. Each key carries a description, its default, whether it can be set by voice, and whether it applies live or needs a restart.
- **Secrets are never stored in the file.** Keys and tokens are references to the OS keychain (`api_key = "keychain:claude"`).
- **Comments survive.** When Footman edits the file (for example after a voice command), it uses a format-preserving TOML editor, so the user's comments and ordering are kept.
- **Versioned:** `version = 1` at the top, with automatic migrations when the schema changes.
- **Live reload** when the file changes, for keys that support it.

```toml
version = 1

[user]
name = "Ian"
language = "en"

[personas]
default = "ashford"
listening = ["ashford"]          # more than one = several personas at once
[personas.overrides.ashford]
title = "Master"
quips = "often"

[input]
dictate = { key = "F8", mode = "hold" }                  # hold | toggle
command = { key = "Shift+F8", mode = "hold" }
wake_word = true
microphone = "default"

[dictation]
cleanup = "simple"               # off | simple | smart
commands = "simple"              # off | simple | smart
insert = "auto"                  # auto | type | paste

[replies]
speak = "when-voice"             # always | when-voice | never
notify = true                    # also show spoken replies as a notification
quiet_hours = "22:00-07:00"

[speech]
stt = { engine = "auto", model = "auto", accelerator = "auto" }   # or whispercpp | sherpa-parakeet; cpu | gpu | vulkan | cuda
tts = { engine = "persona" }     # persona | elevenlabs | …
unload_after = "10m"             # unload speech models after this idle time; 0s = never (ADR-0028)
vad = { min_silence = 0.5 }      # seconds of silence that end an utterance

[ai]
mode = "cloud"                   # cloud | local-first | offline
provider = "claude"
[ai.providers.claude]
api_key = "keychain:claude"
model = "claude-haiku-5-5"
[ai.providers.ollama]
url = "http://localhost:11434"
model = "llama3.2"
[ai.budget.claude]
monthly_usd = 10
warn_at = 0.8

[context]                        # what the AI may see besides your words
local = ["system"]               # e.g. ["system", "window_title", "clipboard", "selection"] or ["all"]
cloud = ["system"]               # system: OS, desktop, hardware, time (ADR-0043)

[memory]
enabled = false

[history]
transcripts = true
retention_days = 30

[permissions]                    # auto | confirm | deny, per level
read = "auto"
act = "auto"
destructive = "confirm"
outward = "confirm"
action_log = true
action_log_retention_days = 90

[jobs]
max_concurrent = 2
worker = "claude-code"           # builtin | claude-code | codex
code_edits = { new_branch = true, show_diff = true }

[coaching]
enabled = false

[web]
listen = "127.0.0.1:7878"        # restart required

[plugins.troupe]                 # one table per installed plugin, keys from its manifest schema
enabled = true
url = "https://troupe.example.com:8443"
token = "keychain:plugin.troupe.token"
```

**Profiles (phase 4):** named sets of settings you can switch between by voice ("Alfred, work mode"), for example work, home, and gaming.

## 29. Security model (draft)

### Threats we design for
| Threat | Example |
|---|---|
| Someone else speaking | A coworker, a guest, a kid. |
| Played-back audio | A TV, a video, or a speakerphone saying "Alfred, delete…". |
| Prompt injection | A web page, email, Teams message, or file the AI reads that says "ignore your instructions and…". |
| Malicious plugin or persona pack | A plugin that reads files it shouldn't; a persona style prompt carrying instructions. |
| Other local processes and users | Another program or user calling the local API. |
| Network attackers | Once remote or phone access is enabled. |

### Who is speaking (speaker recognition)
- **Setup enrolls the owner's voice** with a short sample, using an on-device speaker-recognition model (sherpa-onnx speaker embeddings).
- **An unknown voice gets asked about:** "Should I allow this person?" Someone must **physically click yes** on screen; a voice "yes" doesn't count.
- An allowed guest gets a limited permission level for a limited time (see open questions).
- Until speaker recognition ships, every voice is treated as the owner, and the click-to-confirm rules below are the safety net.

### Confirmations
| Action | How it's confirmed |
|---|---|
| Read | No confirmation. |
| Act | Per config (default: none). |
| Destructive or outward | Voice "yes" or a click (per config). |
| **Security-sensitive changes**: loosening permissions, what the AI may see, enabling remote access, granting plugin permissions, allowing a new speaker | **A physical click only.** Voice never counts, because voice can be spoofed or played back. Tightening security takes effect immediately by voice. |

A click is a desktop notification action or a web UI button (later, a paired phone).

### Untrusted content rule
Anything that enters the AI's context from outside (web pages, messages, emails, file contents, plugin output) is **untrusted data**. Once untrusted content is in the current task's context, **every outward or destructive action in that task needs confirmation**, whatever the config says. The confirmation shows exactly what will be sent or deleted. This blocks the common "the email told the AI to forward your files" attack without tracking content in detail.

### Local API
- **Unix socket** with user-only file permissions (0600), in a directory only the user can enter (0700). A socket client is treated as the owner at the keyboard, with the same trust as editing `config.toml`: it may change security-sensitive settings, since voice and the AI never reach the socket directly (ADR-0025).
- **HTTP** binds to 127.0.0.1 only and requires a token (set as a cookie by `footman open`). It checks Host and Origin headers to block web pages from calling it (CSRF / DNS rebinding).
- **Remote and phone access (later):** pairing by QR code, one revocable token per device, and only over a tunnel or private network.

### Plugins and workers
- **Sandbox (Linux: bubblewrap).** File access is limited to the manifest's paths plus the plugin's data folder. Network is on or off. Per-host filtering through a local proxy comes later.
- **Plugins never see the user's secrets** except the ones in their own config, injected at start.
- **Worker agents** (Claude Code, Codex) run in a git worktree with restricted permission settings that Footman passes in.

### Secrets and privacy
- API keys live in the OS keychain, are never sent to the AI, and are redacted from logs, transcripts, and the action log.
- Wake-word and speaker detection run on the device. **Audio is not stored by default.** A tray icon always shows whether the mic is off, listening for the wake word, or recording.

### Action log
Append-only. Each entry records: **who** (recognized speaker), **what** (tool and arguments, with secrets redacted), **result**, **how it was confirmed**, and **why** (the request that led to it).

### Break-glass stop (ADR-0017)
"Alfred, stop everything" or "Alfred, cancel everything" (**wake word required**, so saying "stop everything" to someone on a call does nothing). It is handled first, ahead of everything else, so it always works instantly. A hotkey and the tray icon do the same without the wake word. All three cancel every job, stop speech, and mute the mic.

### Guests
Once the owner clicks to allow a guest, the guest can do read-level and harmless actions (questions, weather, music, opening a page). Anything that changes, deletes, or sends still needs the owner's click. The permission lasts until the end of the day. The owner can save a guest as a named regular ("always allow").

### Updates
Releases are signed (checksums plus a signature). Footman *notifies* you about updates and installs one only when asked ("Alfred, update yourself"). It never updates silently.

## 30. Local API (ADR-0018, ADR-0025)

- **JSON-RPC 2.0** everywhere: the CLI over the Unix socket (named pipe on Windows); the web UI, phone, and third-party clients over a **WebSocket**.
- **Events** (pipeline state, job progress, confirmations needed, transcripts) are pushed as JSON-RPC notifications on the same connection. Clients subscribe by topic.
- **Client audio** is designed in now: a client can open an audio session and stream mic frames as binary WebSocket messages. This will be implemented in the phone phase.
- **Public, versioned API (`v1`)**, documented for third-party clients and marked experimental until Footman 1.0.
- Authentication is per §29 (socket file permissions; token plus Origin/Host checks for HTTP; per-device tokens for remote access).
- **Wire details (ADR-0025):** on the socket, one JSON message per line (newline-delimited JSON, at most 1 MiB); requests on a connection run concurrently and are matched by `id`. Events arrive as `event` notifications `{subscription, topic, seq, at, data}` after `events.subscribe {topics}`, where a topic is a name, a prefix like `pipeline.*`, or `*`. Event payloads are API types with snake_case fields and string enums, separate from the core's Go types. Footman error codes: `-32000` failed, `-32001` busy, `-32002` not found, `-32003` unavailable. The reference for clients is [`docs/api/v1.md`](api/v1.md).
- **The daemon** (`footman daemon`) wires config → store → bus → pipeline → API. Each area uses the implementation the config names when it is registered; optional areas without one are left out, and the daemon refuses to start without a platform and speech-to-text. `--dev-fakes` (development and tests only) lets every area fall back to its fake instead. SIGTERM shuts down gracefully and removes the socket.

Method groups (sketch):
| Group | Examples |
|---|---|
| `status.*` | `status.get` |
| `events.*` | `events.subscribe`, `events.unsubscribe` |
| `say.*` | `say.text` (send a command, dictation or wake-word utterance as text), `say.audio.start` / `say.audio.end` |
| `jobs.*` | `jobs.list`, `jobs.get`, `jobs.cancel` |
| `confirm.*` | `confirm.list`, `confirm.answer` |
| `config.*` | `config.get`, `config.set`, `config.schema` |
| `plugins.*` / `personas.*` | `list`, `install`, `remove`, `enable` |
| `history.*` / `memory.*` | `list`, `search`, `delete` |
| `system.*` | `system.stop_everything`, `system.update` |

## 31. TTS bake-off plan

**Goal:** choose the small runtime voice model (ADR-0013) and confirm the dataset pipeline works.

**What Troupe already taught us** (its `spikes/engine-audition`, `tts-bakeoff`, `voice-design-studio`):
- IndexTTS-2.5 (RTF ≈ 0.43, peak ≈ 12.7 GB VRAM), VoxCPM2 (≈ 0.52, ≈ 14.6 GB), and Fish S2 Pro q6_k (≈ 1.2, ≈ 13 GB) are all too heavy for Footman's runtime, but **good dataset generators**.
- "Shift then clone" (pitch/formant-shift a real clip, then clone it) beat pure text design in the owner's blind test. Pure text design tends to produce a different-sounding person.
- Qwen3-TTS Base cloning fell into repetition, so it isn't used.
- Troupe has blind-listening tooling (`troupe.listening`) and a voice analyzer (speaker embeddings, accent, pitch) that we can reuse.

**Steps**
1. **Source voice:** design Ashford (ElevenLabs Voice Design or Resonance Lab), and keep about a minute of reference audio.
2. **Dataset:** a phonetically balanced script of about 1,500–3,000 sentences, rendered by Troupe with IndexTTS-2.5 or VoxCPM2 overnight. ASR-verify every line, and drop lines that fail or drift (Troupe's voice-drift check).
3. **Train the candidates** on the same dataset:
   - **Kokoro / StyleTTS2** fine-tune.
   - **Piper** (VITS) fine-tune from a base checkpoint.
   - Optional: any small model Troupe finds promising in the meantime.
4. **Measure** on three machines:
   - RTX 4080 GPU.
   - Ryzen 9 7900X, CPU only.
   - **ASUS ZenBook (Windows, Intel Core Ultra 7 155H: Arc iGPU + NPU)**: the integrated-GPU and low-power reference.
5. **Listen:** a blind A/B web page (reusing Troupe's listening tooling) compares each candidate with the reference clips. The owner listens first; friends later.

**Pass bar**
| Metric | Target |
|---|---|
| Time to first audio | < 300 ms GPU, < 600 ms CPU |
| Real-time factor | < 0.3 on CPU |
| Blind preference vs. reference | Candidate chosen ≥ 35% of the time |
| Size | Voice model ≤ ~350 MB |

**Licensing gate:** check the licenses of every source (ElevenLabs terms on using its outputs, and the IndexTTS-2.5, VoxCPM2, and S2 Pro weights) before any trained voice ships publicly. Personal use isn't blocked.

## 32. Planning

Work is tracked as **sprints = GitHub milestones** (Sprint 0–10; phases map onto them), task issues with `type:` / `status:` / `model:` labels, and the [Footman project board](https://github.com/users/irobinson010/projects/3). Each sprint has a plan in [`docs/sprints/`](sprints/). Future sprints have one epic issue that is broken into tasks at the previous sprint's wrap-up.

## 33. Open areas (next)

- [ ] Trademark search for "Footman" before public launch.


