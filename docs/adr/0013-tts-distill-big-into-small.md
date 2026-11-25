# 0013. TTS: distill big voices into small models

- Status: Proposed
- Date: 2026-11-24

## Context
High-quality voice-cloning models (S2 Pro and similar) are heavy and slow; tiny models are fast but generic.

## Decision
Use a large voice source **offline** to generate a dataset in the target voice, then fine-tune a **small, fast model** (Kokoro/StyleTTS2 or Piper) for runtime. Add pre-generated clips for frequent lines and stream replies sentence by sentence. A bake-off decides the small model.

### Dataset pipeline (updated 2026-11-24)
The cheapest high-quality path reuses Troupe: **design** a voice (ElevenLabs Voice Design or Troupe / Resonance Lab's design pipeline) → take a **short reference clip** (about a minute, which fits small ElevenLabs quotas) → have **Troupe's cloning engines** (IndexTTS-2.5 or VoxCPM2, both about 0.43–0.52× real time on an RTX 4080) generate the **1–3 h dataset** overnight → **fine-tune the small model**. Troupe's measurements show these engines need 12–15 GB of GPU memory, so they're fine for one-off dataset generation but not for Footman's runtime.

## Consequences
Pending the bake-off and a license review of each source's terms on using outputs for training.

See ADR-0019 for the license review: only VoxCPM2-generated data (with a clean reference clip) and permissive base checkpoints may be used for **shipped** voices.
