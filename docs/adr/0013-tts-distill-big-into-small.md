# 0013. TTS: distill big voices into small models

- Status: Proposed
- Date: 2026-11-24

## Context
High-quality voice-cloning models (S2 Pro and similar) are heavy and slow; tiny models are fast but generic.

## Decision
Use a large voice source **offline** to generate a dataset in the target voice, then fine-tune a **small, fast model** (Kokoro/StyleTTS2 or Piper) for runtime. Add pre-generated clips for frequent lines and stream replies sentence by sentence. A bake-off decides the small model.

## Consequences
Pending the bake-off and a license review of each source's terms on using outputs for training.
