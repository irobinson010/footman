# 0038. Permission gate and action log

- Status: Accepted
- Date: 2026-11-26

## Context
Until now `core.DefaultGate` refused every Destructive and Outward tool, the `[permissions]` config keys did nothing, and nothing wrote to the `action_log` table or pruned old rows. Design §14 and §29 and ADR-0015 say how permissions should work; issue #82 makes them real. The click-confirmation broker that actually asks the user (#83) is built in parallel against the shared `permissions.Request`, `Decision` and `Confirmer` types.

## Decision

**Gate.** `permissions.Gate` implements `core.Gate` and a new optional `core.Decider`, which also returns *how* a call was decided (`auto`, `click`, `voice`, `policy`, `timeout`) so the action log can record it. The daemon wires it in; `core.DefaultGate` stays as the pipeline's fallback when no gate is given (the default policy with nobody to ask).
- The policy for a call's level is read from `permissions.read|act|destructive|outward` on every call, so config changes apply at once. `auto` runs it, `deny` refuses it, `confirm` asks the `Confirmer`.
- **Untrusted content rule (ADR-0015):** once untrusted content is in the task's context, a Destructive or Outward call with policy `auto` is treated as `confirm`, and the request's `Reason` says why. `deny` stays `deny`.
- A confirmation `Request` has `Kind` `tool`, a short `Title`, and a `Detail` with the tool name and its arguments exactly as they will be sent, indented and **not** redacted: the user must see what would be sent or deleted. `ClickOnly` is false for tool calls: design §29 allows a voice "yes" for destructive and outward actions. Settings that must never change by voice set it (#83).
- The call is refused, and the AI told why in the tool result, when the policy is `deny`, when there is no confirmer or it fails to ask, when the user refuses, or when nobody answers within `Gate.Timeout` (default 2 minutes; `timeout`). A cancelled task refuses the call with the cancellation.
- **Fast-path rules pass the gate too**, as a tool named after the rule with the command text as its argument, so `permissions.act = "deny"` also stops "open YouTube". A refused rule does not run and replies "I can't do that: …".
- Until #83 lands the daemon has no confirmer, so Destructive and Outward calls are still refused by default (as before), now with policy-driven messages.
- The per-plugin override (`plugins.<id>.permissions.*`) is not in this change: tools do not yet carry a plugin id and plugin keys are validated against manifests. Follow-up: #89.

**What is logged.** `permissions.Log` subscribes to the bus (`SubscribeAll`, so events arrive in publish order) rather than being threaded through `core`. Existing events gained fields: `ToolRan.Confirmation`, `FastPathRan.Reply` and `FastPathRan.Confirmation`; a new `settings.changed` topic (`core.SettingChange`) reports setting changes. Each entry records:
- **who**: `owner` for spoken requests (until speaker recognition), `api` for text from a socket client. The log tells them apart by whether the turn was transcribed before it was routed.
- **what**: `tool` (an AI tool call: name and arguments), `fast_path` (the rule), or `setting` (the key, and `old → new`).
- **result**: the tool's output, the rule's reply, or `error: …` (a refusal reads `error: refused: …`).
- **how**: the gate's decision, or for settings `voice`, `auto` (the local API, which is trusted as the owner at the keyboard, ADR-0025, or a hand edit of the file) and later `click`.
- **why**: the routed request text; for settings `changed by voice`, `config.set` or `config file edited`.

Setting changes come from three places: `Settings.Set` (voice and the AI's `set_setting` tool) publishes one event; `config.set` and the config watcher publish one per key that differs between the old and new config, so a hand edit is logged too and a change made through the API or by voice is not logged twice (the watcher's reload then matches what is already loaded). `start_task` is logged with `auto`; it is not gated yet.

`permissions.action_log = false` records nothing (read per event). Free-text fields are capped at 2000 bytes.

**Redaction.** Before anything is written:
- Tool arguments are parsed as JSON; any value under a secret-sounding name (`password`, `token`, `secret`, `api_key`, `authorization`, `cookie`, `credential`, `private_key`, …) becomes `[redacted]`, and every string is redacted as text.
- Text (arguments, results, request text) loses keychain references (`keychain:<name>`), known key and token shapes (`sk-…`, GitHub, AWS, Slack, Google and GitLab tokens, JWTs, bearer tokens, `Authorization: Basic` credentials, PEM private keys) and any run of 32 or more key characters that mixes upper case, lower case and digits. Words, paths, URLs and hex hashes are kept.
- Setting changes to `Secret`-typed keys, and to keys named like secrets, keep only whether a value was set.

Redaction is best effort for free text; the structural rules (secret field names, Secret config keys) are exact.

**Retention.** The daemon runs `store.Prune` at start and then daily, with ages read from the config each time: `permissions.action_log_retention_days` (0 keeps entries forever) and `history.retention_days` (0 keeps no transcripts, as its description says).

**API and CLI.** `actions.list {limit}` (default 20, at most 1000, newest first) and `footman actions [-n N] [--json]`, which prints oldest first so the latest is at the bottom.

## Consequences
- Destructive and outward tools now follow the user's config, and the whole confirm path is testable with a fake `Confirmer`; #83 only has to supply the broker and set `gate.Confirmer`.
- When #83 applies a setting after a click, it should publish `settings.changed` with `how = "click"` (or go through `config.set`-like code that does).
- The action log is asynchronous: an entry is written shortly after its event, and events dropped by a full bus buffer (256) would be missing. That is acceptable for a local, low-rate stream; if it ever matters the log can move to a synchronous hook.
- The actor is inferred from events; speaker recognition will replace `owner` with the recognized speaker by adding a field to the transcript event.
