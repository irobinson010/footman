# 0010. Runtime accelerator selection

- Status: Accepted
- Date: 2026-11-24

## Context
Users run NVIDIA, AMD, Intel, Apple Silicon, ARM, or no GPU at all. Separate builds per vendor don't scale.

## Decision
Engines report the accelerators they support. `footman setup` detects the hardware, benchmarks briefly, and picks one: CUDA, Vulkan (whisper.cpp, the universal GPU path), ROCm, DirectML, Metal/CoreML, OpenVINO, or CPU, with smaller models on CPU. Users can override the choice. A pure-Go `nocgo` build offers cloud speech only.

## Consequences
We need at least two STT engines (sherpa-onnx and whisper.cpp).
