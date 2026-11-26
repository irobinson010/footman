# 0036. Persona pack format as built

- Status: Accepted
- Date: 2026-11-26

## Context
Design §27 sketched persona packs. Sprint 3 builds the loader, the four shipped personas (ADR-0012) and the commands to manage packs. A pack is text from the internet, and its voice must be named in a way the TTS engines (S3-02) can use without a pack carrying audio (ADR-0020).

## Decision
**Layout.** A pack is a folder: `persona.toml`, `style.md` (path set by `style.prompt`), `lines.<lang>.toml`, and optionally `voice/`, `wake/` and `clips/`. `internal/persona` loads it into a `Pack` and validates it; it reports every problem at once.

**Strict and bounded.** Unknown keys in `persona.toml` or a lines file are errors, so a pack cannot declare tools, permissions or config. Enums are checked (`verbosity`: brief | normal | chatty; `quips`: never | sometimes | often), numbers range-checked, IDs match `[a-z][a-z0-9-]{0,31}`. Every path in the manifest must be a relative path inside the pack and must exist. Folders loaded from disk are refused if they contain symbolic links or special files. Limits: `persona.toml` 64 KiB, `style.md` 8 KiB, a lines file 64 KiB, 240 characters and 64 lines per kind, 2000 files and 512 MiB per pack.

**Voice.** `[voice]` has `engine` (any lowercase name; at launch `kokoro` or `piper`, from the sherpa-onnx engines in S3-02), `model`, `speaker`, `speed` (0.5–2, default 1) and optional `cloud = { provider, voice_id }`. `model` is a pack-relative file (it contains a `/` or has an extension) or a models-catalog ID (it has neither), so the shipped packs name a stock interim voice and hold no audio. `speaker` is a name or an index, stored as a string. The engine rejects engines or models it does not have; the loader does not know the catalog.

**Pronunciation (addendum, 2026-11-26).** `[pronounce]` maps a word or phrase to an English respelling: `jawohl = "yah vole"`. Keys are 1-4 words, at most 40 characters, letters with inner apostrophes or hyphens, matched case-insensitively as whole words (longest phrase first); values are 1-80 characters of plain words made from letters, spaces, apostrophes and hyphens (no markup, digits or punctuation); at most 128 entries; duplicates after lower-casing are errors. The loader exposes it as `Pack.Pronounce`; the daemon copies it to `speech.Voice.Respell`, and the sherpa engine applies it to the normalized text before phonemizing (ADR-0037). Engines that do not phonemize themselves ignore it. The clip bank's cache key includes the table, so changing a respelling re-renders every clip. The stock voices phonemize in English and spell out any word missing from their lexicons letter by letter, so a pack that uses foreign words or invented names must respell them; `TestShippedLinesNeverSpelled` (sherpa package, needs the Kokoro and espeak lexicons) fails if any line of a shipped pack still gets spelled. Follow-up: a rule-based letter-to-sound guess would let unknown lower-case words be spoken approximately, limiting spelling to short ALL-CAPS tokens (acronyms); it is not built yet.

**Address.** `form` may use only `{title}` and `{name}` (default `{title} {name}`). `Pack.Address(cfg)` fills the name from `user.name` and the title from `personas.overrides.<id>.title`, and returns `address.fallback` until a name is set. An empty title gives just the name.

**Lines.** Kinds are `ack`, `working`, `done`, `confirm`, `error`, `quips`. `{address}` is the only placeholder. Lookup falls back from the language to its base language to English; `en` is required. `AddressLines` and `StaticLines` split lines by whether they contain `{address}`, for the clip bank. `Chooser` picks at random without repeating the previous line of a kind.

**Style.** `SystemPrompt(coreRules)` returns a `<persona-style>` section (a preamble saying it is a style guide for spoken and chat replies only, the pack's settings, then `style.md`), followed by the core rules under a heading saying they win. Delimiter-like text and the phrase "core rules" are removed from the style so it cannot close its own section or pose as the rules.

**Where packs live.** The shipped packs are embedded from `personas/`. User packs are in `<data>/personas/<id>/`, and the folder name must equal the ID. A user pack replaces a shipped one of the same ID. `footman persona list | install [--replace] <dir> | remove <id>`; install validates, copies to a temporary folder and renames it into place, then records the pack in the install receipt (`GroupPersonas`, ADR-0023). Shipped packs cannot be removed. Archive and URL installs are later.

## Consequences
- Hostile packs fail at load, and injection text stays inside the style section; the core rules are the model's last word on safety. This does not stop a style from steering tone within its section, which is its job.
- Adding a language or a line kind is a format change here, because unknown keys are errors.
- `footman persona remove` does not edit the install receipt; uninstall tolerates a missing path.
- The engine and speaker names in the shipped packs follow S3-02's catalog and may need adjusting when it lands.
