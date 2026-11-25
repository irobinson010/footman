# 0020. Voice sourcing: quality first during development

- Status: Accepted
- Date: 2026-11-24
- Amends: 0019

## Context
ADR-0019 found that ElevenLabs, IndexTTS-2.5, and Fish S2 Pro all restrict using their outputs to train models that are distributed publicly, and that only the VoxCPM2 pipeline is clean for shipped voices. The owner has decided that, for now, licensing should not hold back voice quality. The project is non-commercial, and the immediate goal is to find out what sounds good.

## Decision
- During development and the bake-off, **any source can be used** to build and test persona voices: ElevenLabs, Troupe's IndexTTS-2.5 / VoxCPM2 / S2 Pro, and any base checkpoint.
- Every trained voice records its **provenance** (source engine, reference audio, dataset text, base checkpoint) in a model card, so its licensing can be judged later.
- ADR-0019 stays as the reference for what is clean. **Before a release ships voices publicly**, revisit the choice: keep the voices, retrain them on the ADR-0019 pipeline, or distribute them separately.
- Until then, trained voice weights are kept out of the git history (they are stored as release assets or downloaded), so a later change of course doesn't require rewriting history.

## Consequences
The bake-off can use the best-sounding sources right away. One licensing decision is deferred to the first release that ships voices, and provenance tracking makes that decision possible later.
