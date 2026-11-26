# Sprint 3: Persona voice, TTS & permissions

**Milestone:** [Sprint 3 — Persona voice, TTS & permissions (M2)](https://github.com/irobinson010/footman/milestone/4) · **Epic:** [#17](https://github.com/irobinson010/footman/issues/17) · **Board:** [Footman project](https://github.com/users/irobinson010/projects/3)

**Goal:** the Butler talks back, safely. Persona packs and the four shipped personas, a local TTS voice, the clip bank and sentence streaming; the permission gate, click confirmations and the action log.

**Exit criteria**
- Spoken acknowledgement in under 300 ms (median, end of speech to first ack sample, on the workstation).
- Security-sensitive voice changes require a click; tightening security by voice takes effect at once.
- Destructive and outward tools run only after confirmation, and every action is in the action log with secrets redacted.
- Sprint 4 is broken into task issues.

## TTS without waiting for the GPU

The bake-off part 2 ([#10](https://github.com/irobinson010/footman/issues/10)) trains the small Ashford model on the 1.6 h VoxCPM2 dataset (S0-09), and needs the GPU, which is busy. Sprint 3 doesn't wait for it:
- S3-02 adds a sherpa-onnx TTS engine that loads both bake-off candidate families, **Kokoro** and **Piper/VITS** (ADR-0013, ADR-0019), and ships stock English voices as an **interim** default, chosen on the CPU against the §31 pass bar (ADR-0037).
- Persona packs name their voice as an engine, a model and a speaker. The trained Ashford model replaces the stock voice by changing the pack's model, with no code change.
- The < 300 ms acknowledgement comes from the clip bank (pre-rendered, held in memory), not from live synthesis.

## Lanes

| Lane | Tasks |
|---|---|
| A — persona | S3-01 → S3-03 |
| B — voice | S3-02 |
| C — replies | S3-04 |
| D — permissions | S3-05 → S3-06 (S3-06 needs only S3-05's types PR) |
| E — integration | S3-07 (after S3-01 to S3-04; S3-05 and S3-06 for the full exit check) |
| wrap-up | S3-08 |

**Start at once, in parallel:** S3-01, S3-02, S3-04, S3-05 (and S3-06 against the types in S3-05's issue). No task needs the GPU: TTS is measured on the CPU only.

ADR numbers are reserved to avoid collisions: 0036 persona packs (S3-01), 0037 interim TTS (S3-02), 0038 permission gate and action log (S3-05), 0039 confirmations (S3-06).

## Tasks

| ID | Task | Lane | Depends on | Human |
|---|---|---|---|---|
| [S3-01](https://github.com/irobinson010/footman/issues/78) | Persona packs: loader, address forms, and the four shipped personas | A — persona | none | no |
| [S3-02](https://github.com/irobinson010/footman/issues/79) | TTS engine: interim sherpa-onnx voice (Kokoro / Piper) | B — voice | none | no |
| [S3-03](https://github.com/irobinson010/footman/issues/80) | Clip bank: pre-rendered and personalized acknowledgement clips | A — persona | S3-01 | no |
| [S3-04](https://github.com/irobinson010/footman/issues/81) | Sentence streaming and the reply policy | C — replies | none | no |
| [S3-05](https://github.com/irobinson010/footman/issues/82) | Permission gate and action log | D — permissions | none | no |
| [S3-06](https://github.com/irobinson010/footman/issues/83) | Click confirmations; tighten by voice, loosen by click | D — permissions | S3-05 (types) | no |
| [S3-07](https://github.com/irobinson010/footman/issues/84) | The Butler talks back: end-to-end wiring, voices picked, latency measured | E — integration | S3-01 to S3-04 | yes (pick voices, real-desktop check) |
| [S3-08](https://github.com/irobinson010/footman/issues/85) | Sprint 3 wrap-up | wrap-up | all | yes (sign-off) |

## What already exists (extend, don't duplicate)
- `speech.TTS` and the `speech.TTSs` registry, a fake TTS, and idle unloading for TTS (`cmd/footman/unload.go`).
- `core.Gate` and the interim `core.DefaultGate` (refuses Destructive and Outward), with the untrusted flag threaded through `converse`.
- `config.VoicePolicy` (`VoiceNeedsClick`) and the `settings.confirm` bus event from settings by voice; nothing yet shows or answers it.
- `permissions.*` config keys, the `action_log` table with `store.AppendAction`, `RecentActions` and `Prune` (nothing writes to it or prunes it yet).
- `platform.Notifier` with click actions; the pipeline's `Acknowledging` state and single `Config.Ack` clip.
- `internal/persona` and `internal/permissions` are empty packages.
