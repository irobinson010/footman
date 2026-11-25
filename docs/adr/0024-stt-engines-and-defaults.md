# 0024. Speech-to-text engines and defaults per hardware tier

- Status: Accepted (ZenBook / integrated-GPU numbers pending)
- Date: 2026-11-25

## Context
Spike S0-07 (`spikes/stt-latency/REPORT.md`) measured sherpa-onnx and whisper.cpp with Whisper base/small/turbo and Parakeet TDT 0.6B on an RTX 4080 (CUDA and Vulkan) and a Ryzen 9 7900X (CPU).
- whisper.cpp turbo returns 10 s of speech in about 120 ms with CUDA and about 155 ms with Vulkan.
- Parakeet on CPU takes about 470 ms, with the lowest error rate.
- whisper.cpp base.en on CPU takes about 610 ms in about 260 MB of RAM.
- Large Whisper models on CPU take 5–8 s.

## Decision
`footman setup` picks the default from the hardware it detects (ADR-0010). The user can override it.

| Hardware | Engine | Model | Accelerator |
|---|---|---|---|
| Any discrete or integrated GPU with Vulkan | whisper.cpp | whisper-turbo (q5) | **Vulkan** (CUDA optional on NVIDIA) |
| CPU only, ≥ 8 GB RAM | sherpa-onnx | Parakeet TDT 0.6B v2 (English) | CPU |
| CPU only, < 8 GB RAM | whisper.cpp | whisper base.en | CPU |
| Non-English language selected, no GPU | whisper.cpp | whisper small | CPU |

- **Vulkan is the primary GPU path.** One build covers NVIDIA, AMD, and Intel, at about 70–80% of CUDA's speed. Setup runs a warm-up so the one-time shader compile (8–20 s) never happens mid-dictation, and the shader cache persists in the cache folder.
- **CUDA builds** are an optional extra for NVIDIA users.
- **sherpa-onnx's GPU runtime** needs a CUDA 12.5+ runtime and cuDNN 9; it is not shipped by default.

## Consequences
Two STT engines to maintain, behind one interface. Release builds need a whisper.cpp Vulkan build per platform, and the cgo build gains cmake. The integrated-GPU (ZenBook, Intel Arc) results may adjust the turbo-vs-small choice for weaker GPUs; this ADR is updated when they land.
