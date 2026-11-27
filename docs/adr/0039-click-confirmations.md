# 0039. Click confirmations: broker, channels, expiry, and tighten by voice / loosen by click

- Status: Accepted
- Date: 2026-11-26
- Amended by: 0047

## Context
ADR-0038 added the permission gate, which asks a `permissions.Confirmer` before a call whose policy is `confirm`, but the daemon had no confirmer, so every such call was refused. Settings by voice refused security-sensitive changes and published `settings.confirm`, which nothing showed or answered. Design §29 says security-sensitive changes need a physical click and that tightening security takes effect at once by voice. Issue #83 builds the confirmation flow.

## Decision

**Broker.** `permissions.Broker` holds pending confirmations. It implements `permissions.Confirmer` for the gate (blocking until answered) and `core.SettingConfirmer` for settings by voice (`AskSetting` returns the request ID at once and calls back with the answer, so the spoken reply can name the ID and the pipeline is not held up for two minutes).
- Each request gets a short ID from the broker (`1`, `2`, …, easy to type), an expiry (`Broker.Timeout`, default 2 minutes) and is published on `confirm.request`. An unanswered request is refused at expiry with how `timeout`. A request whose asker gives up (its task cancelled) or that is pending when the daemon stops is refused as `cancelled`. Every end is published on `confirm.resolved {id, allowed, how}`.
- `confirm.request` and `confirm.resolved` replace `settings.confirm` (a contract change, recorded in `docs/api/v1.md`).
- A request's `Detail` is shown unredacted, as ADR-0038 requires; it is never written to the action log, which records the outcome through the existing events.

**Click channels.** A click is a physical confirmation:
- A **desktop notification** with Allow and Deny buttons (`platform.Notifier` actions). Closing the notification without a button leaves the request pending, so it can still be answered from the CLI until it expires. When the request is answered elsewhere, the broker stops waiting on the notification; the Linux notifier does not yet withdraw it from the screen, and a late click on it does nothing.
- The **local API**: `confirm.list` and `confirm.answer {id, allow}`. A socket client is the owner at the keyboard (ADR-0025), so its answer counts as a click, also for click-only requests.
- The **CLI**: `footman confirm` lists, `footman confirm allow|deny <id>` answers (through the API).
- Later: the web UI button (Sprint 5) and a paired phone.

`Broker.Answer(id, allow, how)` takes `click` or `voice`. A voice answer to a `ClickOnly` request fails with `ErrClickOnly` and leaves it pending. **For this sprint confirmations were click-only in practice** (ADR-0047 now calls `Answer` with `voice` for spoken yes and no on Act and Outward tool calls; see there). Originally: a spoken "yes" for destructive and outward tool calls (allowed by design §29, and why ADR-0038 left `ClickOnly` false for tool requests) was left as a follow-up: it needs speaker recognition or at least a check that the "yes" came from the owner's microphone in the same task, and a persona prompt for it. It calls `Answer(id, allow, "voice")`, and setting requests stay click-only. The AI has no tool that answers a confirmation, and a spoken "yes" reaches the router as an ordinary utterance.

**Settings by voice.** `core.Settings.Set` parses and validates the value first (an invalid value is refused before anything is asked), then asks `config.VoiceChange(path, old, new)`:
- Allowed: applied at once, published on `settings.changed` with how `voice`.
- Needs a click: a click-only broker request (`Kind` `setting`, `Detail` `key: old → new`). The spoken reply says why and how to confirm ("click Allow on the notification, or run footman confirm allow 2"; the persona's `confirm` line replaces it when #84 wires personas). Nothing changes until Allow; the change is then applied exactly as asked (the same parsed value through the same `Store.Set` validation as `config.set`), published on `settings.changed` with how `click` and actor `owner`, and the outcome is shown as a notification. Deny or timeout changes nothing.
- Never: secrets are not set by voice at all, not even with a click; speaking one would already leak it. The reply points to `footman config set`.
- With no broker (tests, or a build without it) a loosening is refused with nothing to click.

The daemon now wires settings by voice (the `settings` fast-path rule and the `get_setting`, `set_setting`, `list_settings` AI tools), which earlier work had built but not wired.

**Tighten by voice, loosen by click.** `config.VoiceChange` refines `config.VoicePolicy` with the direction of the change (`config.Tightens`). A change to a security-sensitive or never-by-voice key that tightens, or changes nothing, is allowed by voice:
- `permissions.read|act|destructive|outward` towards `deny` (`auto` < `confirm` < `deny`);
- `permissions.action_log` on;
- `memory.enabled` off;
- `context.local` / `context.cloud` losing items without gaining any (`all` counts as every current item; changing a list to `all` is a loosening, since `all` also covers items added later);
- `ai.mode` towards `offline` (`cloud` < `local-first` < `offline`).

The reverse of each needs a click. Keys with no safe direction (`web.listen`, `ai.providers.*`, `ai.budget.*`, `permissions.action_log_retention_days`, plugin settings, and keys not marked voice-settable) need a click for any change. `VoicePolicy` keeps its direction-free meaning, for listing what voice may change.

## Consequences
- Destructive and outward tools with policy `confirm` (the default) now run after a click on the notification or `footman confirm allow`, instead of being refused.
- "Turn off the action log" by voice waits for a click; "set destructive actions to deny" applies at once. New spoken aliases: "action log", "read / destructive / outward actions", "memory".
- Turning the action log off is not itself recorded: the log reads `permissions.action_log` when it processes the event, and by then it is off. Worth revisiting in the action log (record changes to `permissions.action_log` regardless).
- A confirmation's answer and the change it allows are asynchronous: `confirm.answer` returns once the decision is delivered, and a setting is applied just after. Clients that need the outcome watch `settings.changed` or `confirm.resolved`.
- Pending confirmations live in memory: a daemon restart refuses them all.
