# 0042. The persona talks back: wiring, live reload, replies spoken and shown

- Status: Accepted
- Date: 2026-11-26

## Context
Sprint 3 built the pieces separately: persona packs (ADR-0036), an interim CPU voice (ADR-0037), the clip bank (ADR-0040), sentence streaming and the reply policy (S3-04), and the permission gate with click confirmations (ADR-0038, ADR-0039). S3-07 (#84) puts them together in the daemon. Before it, `speech.tts.engine = "persona"` resolved to no engine at all, so replies were only ever shown, the AI had no system prompt, and the spotter listened for the bare persona ID.

The owner decided on 2026-11-26 that replies are **spoken by the persona and shown as a notification**, both, by default; quiet mode and quiet hours silence the speech but keep the notification. The owner also uses "alfred" as an extra wake word, and picked the interim voices: Ashford `bm_george`, Klaus `bm_lewis`, Pettigrew `bm_fable`, Lucy `bf_emma` (Kokoro, `kokoro-en`).

## Decision
- **One active persona, `personas.default`.** The daemon loads its pack (falling back to Ashford, with a log line, if it is missing or broken) and gives the pipeline a `core.Persona`: the voice, the system prompt `Pack.SystemPromptWith(core.CoreRules, quips)`, and the pack's `done`, `error` and `confirm` lines rendered with the user's address. Lines are used where the pipeline had nothing or a fixed sentence: `done` when a command worked with nothing to say, `error` when it failed (spoken; the notification also carries the error), `confirm` while a click confirmation waits during a spoken command. The acknowledgement stays the clip bank's.
- **TTS engine.** `speech.tts.engine = "persona"` (the default) uses the pack's `[voice]`; naming an engine uses that engine with its own default voice. The daemon keeps one idle-unloading engine per engine name, built when first needed; the engines get `models_dir`. Text-to-speech is optional: if it cannot be built, the daemon logs why and replies are only shown, instead of refusing to start.
- **Wake words** are the `[wake] words` of the listening personas and of the default persona (which is the one that answers), "hey footman", and `speech.wake.extra`. The router strips the same list plus "footman".
- **Live reload.** On every config reload (hand edit, `config.set`, voice) the persona, voice, prompt, lines, clip bank target and wake words are rebuilt. A changed wake list builds a new spotter and restarts the wake microphone session, so `personas.listening`, `personas.default` and `speech.wake.extra` no longer need a restart.
- **Switching by voice.** A fast-path rule matches "switch to Klaus", "use Lucy", "change persona to …", "let me talk to …" for a known persona name (anything else falls through). It sets `personas.default` through `core.Settings` (so it is logged like any voice change) and applies it at once, so the answer ("Klaus here. Jawohl, Herr Alex.") is in the new voice.
- **Replies spoken and shown.** New key `replies.notify` (bool, default on, live, voice-settable): a spoken reply is also shown as a notification titled with the persona's name. A reply that is not spoken (quiet mode, quiet hours, `replies.speak`, a typed request under `when-voice`) is always shown, whatever `replies.notify` says.
- **Downloads.** `footman models fetch` with no IDs and `footman setup` also fetch the default persona's voice model and its lexicon (download only; the voice runs on the CPU).

## Consequences
- The daemon now holds Kokoro (about 0.7 GB) after start until `speech.unload_after`; the clip bank renders the persona's clips on start and after each persona or address change, sharing that engine.
- A switch to a persona on another engine (Piper) builds that engine once, synchronously, before the reply.
- Several personas answering by their own wake word (design §27) is still to come: today every wake word wakes the default persona.
- Measured with the real CPU engines and the fake platform: see `docs/sprints/sprint-03.md`. The owner checks latency and listening on the real desktop.
