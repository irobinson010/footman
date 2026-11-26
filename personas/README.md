# personas

The persona packs that ship in the binary: `ashford`, `klaus`, `pettigrew` and `lucy`, all original characters (ADR-0012). `embed.go` embeds them; `internal/persona` loads and validates them.

A pack is a folder with `persona.toml`, `style.md`, `lines.<lang>.toml` and optionally `voice/`, `wake/` and `clips/`. The format is in ADR-0036 and design §27. No audio and no voice weights are committed (ADR-0020): each pack's `[voice]` names an interim stock voice from the models catalog.

Users install their own packs with `footman persona install <dir>`; they go in `<data>/personas/<id>/` and override a shipped pack of the same ID.
