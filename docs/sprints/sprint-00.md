# Sprint 0: Foundations & spikes

**Milestone:** [Sprint 0 — Foundations & spikes](https://github.com/irobinson010/footman/milestone/1) · **Board:** [Footman project](https://github.com/users/irobinson010/projects/3)

**Goal:** build the skeleton everything else stands on, and answer the risky questions with measurements instead of guesses.

**Exit criteria**
- `go test ./...` passes in under 10 s with no GPU, network, desktop or API keys.
- The walking skeleton (fake audio → fake STT → router → fake platform) passes end to end.
- Every spike has a `REPORT.md` with numbers and a recommendation, and the ADRs are updated from it.
- Sprint 1 is broken into task issues.

Spike code lives in `spikes/<name>/`, is throwaway, and is never imported from `cmd/` or `internal/`.

## Lanes

| Lane | Tasks |
|---|---|
| A — core | S0-01 → S0-02 → S0-05 |
| B — state | S0-03, S0-04 (after S0-01) |
| C — platform | S0-06 |
| D — speech | S0-07, S0-08 |
| E — voice | S0-09 → S0-10; S0-11 in parallel |
| F — docs | S0-12 (after S0-01), S0-13 |
| wrap-up | S0-14 |

GPU tasks (S0-07, S0-09, S0-10) run one at a time and are coordinated with Troupe's GPU use.

## Tasks

| ID | Task | Lane | Depends on | Human |
|---|---|---|---|---|
| [S0-01](https://github.com/irobinson010/footman/issues/1) | Repo scaffold, tooling and CI | A — core | none | no |
| [S0-02](https://github.com/irobinson010/footman/issues/2) | Core interfaces, registries and fakes | A — core | S0-01 | no |
| [S0-03](https://github.com/irobinson010/footman/issues/3) | Config: schema, defaults, validation, comment-preserving edits | B — state | S0-01 | no |
| [S0-04](https://github.com/irobinson010/footman/issues/4) | Store: SQLite and migrations | B — state | S0-01 | no |
| [S0-05](https://github.com/irobinson010/footman/issues/5) | Event bus and pipeline state machine (walking skeleton) | A — core | S0-02, S0-03 | no |
| [S0-06](https://github.com/irobinson010/footman/issues/6) | Spike: global hotkeys and typing at the cursor on KDE Wayland | C — platform | none | yes (test in real apps) |
| [S0-07](https://github.com/irobinson010/footman/issues/7) | Spike: speech-to-text latency across engines and hardware | D — speech | none | yes (run on the ZenBook) |
| [S0-08](https://github.com/irobinson010/footman/issues/8) | Spike: wake word, VAD and break-glass phrase | D — speech | none | yes (room testing) |
| [S0-09](https://github.com/irobinson010/footman/issues/9) | Spike: TTS bake-off part 1 — Ashford dataset via Troupe | E — voice | none | yes (approve the voice) |
| [S0-10](https://github.com/irobinson010/footman/issues/10) | Spike: TTS bake-off part 2 — train and judge small models | E — voice | S0-09 | yes (blind listening) |
| [S0-11](https://github.com/irobinson010/footman/issues/11) | Decision: license review for voice sources | E — voice | none | no |
| [S0-12](https://github.com/irobinson010/footman/issues/12) | Docs skeleton and Mermaid diagrams | F — docs | S0-01 | no |
| [S0-13](https://github.com/irobinson010/footman/issues/13) | Trademark search for “Footman” | F — docs | none | yes |
| [S0-14](https://github.com/irobinson010/footman/issues/14) | Sprint 0 wrap-up | wrap-up | all | yes (sign-off) |

## Test hardware
- **Workstation:** Ryzen 9 7900X, 64 GB RAM, RTX 4080 16 GB, Kubuntu (KDE Wayland).
- **ASUS ZenBook (Windows):** Intel Core Ultra 7 155H (16 cores / 22 threads, Arc integrated GPU, Intel AI Boost NPU). This is the integrated-GPU and low-power reference.
