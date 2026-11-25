# S0-07: speech-to-text latency across engines and hardware

Status: workstation measured on 2026-11-25. The ZenBook (Intel Core Ultra 7 155H) run is pending and will be appended.

## Setup
- **Workstation:** Ryzen 9 7900X (12 cores), RTX 4080 16 GB, Kubuntu 26.04, NVIDIA driver with CUDA 13.2, CUDA toolkit 12.4 for builds.
- **Clips:** six LibriSpeech test-clean clips (about 5, 10 and 30 s, two each).
- **Timing:** three timed runs after a warm-up; the median is reported.
- **GPU state:** Troupe's LLM and director containers held about 5.4 GB of VRAM but were idle.
- **Engines:** sherpa-onnx 2.0.3 (onnxruntime 1.28.2) and whisper.cpp 1.9.6. GPU rows use 4 threads; CPU rows use 12.
- **sherpa-onnx CUDA:** it needs a CUDA 12.5+ runtime (`cudaLibraryGetKernel`); Ubuntu's 12.4 makes it **silently fall back to CPU**. Those rows were re-run with the `nvidia-cuda-runtime-cu12` wheel on `LD_LIBRARY_PATH`.

Latency is the median time from the end of audio to the final text, in ms. WER is measured over only six clips (a few hundred words), so **WER differences under about 2 points are noise**.

| engine | model | accel | thr | load ms | first ms | ~5 s | ~10 s | ~30 s | RTF | WER | model MB | peak RSS MB | peak VRAM MB |
|---|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| sherpa | parakeet-tdt-0.6b-v2 | cpu | 12 | 4719 | 414 | 367 | 471 | 1692 | 0.058 | 2.4% | 631 | 1729 | - |
| sherpa | parakeet-tdt-0.6b-v2 | cuda | 4 | 2230 | 272 | 168 | 314 | 859 | 0.031 | 2.4% | 631 | 2033 | 1334 (own-pid) |
| sherpa | whisper-base.en | cpu | 12 | 1829 | 856 | 553 | 1293 | 3160 | 0.114 | 3.3% | 152 | 702 | - |
| sherpa | whisper-base.en | cuda | 4 | 810 | 339 | 68 | 123 | 344 | 0.012 | 2.9% | 278 | 920 | 1882 (own-pid) |
| sherpa | whisper-small.en | cpu | 12 | 3764 | 2780 | 2329 | 4507 | 13525 | 0.460 | 3.3% | 357 | 1322 | - |
| sherpa | whisper-small.en | cuda | 4 | 1954 | 237 | 108 | 200 | 555 | 0.019 | 3.3% | 924 | 1279 | 4382 (own-pid) |
| sherpa | whisper-turbo | cpu | 12 | 5671 | 2600 | 1987 | 3193 | 7542 | 0.319 | 3.3% | 988 | 2400 | - |
| sherpa | whisper-turbo | cuda | 4 | 1800 | 799 | 644 | 907 | 1606 | 0.091 | 2.9% | 988 | 2177 | 2342 (own-pid) |
| whispercpp | whisper-base.en | cpu | 12 | 328 | 539 | 357 | 612 | 1172 | 0.061 | 2.9% | 141 | 260 | - |
| whispercpp | whisper-base.en | cuda | 4 | 714 | 225 | 40 | 60 | 170 | 0.006 | 2.9% | 141 | 443 | 590 (own-pid) |
| whispercpp | whisper-base.en | vulkan | 4 | 516 | 15826 | 41 | 71 | 247 | 0.008 | 2.9% | 141 | 348 | 339 (own-pid) |
| whispercpp | whisper-small.en | cpu | 12 | 1037 | 1381 | 1248 | 1694 | 3584 | 0.169 | 4.5% | 465 | 653 | - |
| whispercpp | whisper-small.en | cuda | 4 | 1704 | 359 | 62 | 101 | 270 | 0.010 | 4.5% | 465 | 444 | 984 (own-pid) |
| whispercpp | whisper-small.en | vulkan | 4 | 1052 | 1478 | 86 | 156 | 406 | 0.014 | 4.5% | 465 | 242 | 733 (own-pid) |
| whispercpp | whisper-turbo-q5 | cpu | 12 | 651 | 7499 | 7036 | 6838 | 10944 | 0.684 | 5.7% | 547 | 786 | - |
| whispercpp | whisper-turbo-q5 | cuda | 4 | 1955 | 305 | 72 | 110 | 246 | 0.011 | 5.7% | 547 | 463 | 1076 (own-pid) |
| whispercpp | whisper-turbo-q5 | vulkan | 4 | 665 | 20513 | 96 | 147 | 320 | 0.015 | 5.7% | 547 | 487 | 849 (own-pid) |
| whispercpp | whisper-turbo | cpu | 12 | 1960 | 4810 | 5786 | 5164 | 8229 | 0.517 | 5.7% | 1549 | 1785 | - |
| whispercpp | whisper-turbo | cuda | 4 | 3581 | 312 | 73 | 120 | 242 | 0.012 | 5.7% | 1549 | 467 | 2084 (own-pid) |
| whispercpp | whisper-turbo | vulkan | 4 | 998 | 8842 | 95 | 154 | 341 | 0.015 | 5.7% | 1549 | 305 | 1844 (own-pid) |

## Findings
1. **Every configuration meets Sprint 1's "text within about 1 s after a 10 s utterance" except the larger Whisper models on CPU.**
2. **GPU: whisper.cpp is the fastest.** Turbo returns 10 s of speech in about 120 ms on CUDA and about 155 ms on **Vulkan**. Vulkan is within about 30% of CUDA, so **one Vulkan build covers NVIDIA, AMD and Intel GPUs**.
   - **Caveat:** Vulkan's first run compiles shaders and takes 8–20 s ("first ms" column). Setup must warm it up once, and the shader cache must persist.
3. **CPU: sherpa-onnx Parakeet TDT 0.6B v2 is the best all-rounder.** It has the lowest WER (2.4%), takes about 470 ms for 10 s on 12 threads, and is fast with CUDA too (about 315 ms). The cost is about 1.7 GB RSS.
   - For low-RAM machines, **whisper.cpp base.en** uses about 260 MB RSS, takes about 610 ms for 10 s, and has 2.9% WER.
4. **Large Whisper models on CPU are too slow for dictation:** turbo takes 5–8 s, and turbo-q5 about 7 s.
5. **sherpa-onnx with Whisper on CUDA** is slower than whisper.cpp (turbo: 907 ms vs 120 ms for 10 s) but fine for small models.
6. **Build and cgo:**
   - sherpa-onnx ships prebuilt CPU libraries (trivial build), but its GPU libraries need a matching CUDA runtime and cuDNN.
   - whisper.cpp needs a cmake build, and each backend is its own binary (CPU, CUDA, Vulkan).

## Recommendation (ADR-0024)
- **GPU present (any vendor):** whisper.cpp with **Vulkan** by default (CUDA optional on NVIDIA), model **whisper-turbo-q5** (multilingual, ready for Spanish/Japanese, about 850 MB VRAM). Warm up at setup.
- **CPU only, ≥ 8 GB RAM:** sherpa-onnx **Parakeet TDT 0.6B v2** (English). For other languages, whisper.cpp small, which is slower.
- **CPU only, low RAM:** whisper.cpp **base.en**.
- Keep both engines behind the STT interface, as ADR-0010 planned.
