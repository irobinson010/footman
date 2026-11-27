# S0-07: speech-to-text latency harness

Throwaway spike code for issue #7. It is a separate Go module with its own `go.mod`, so
`go test ./...` at the repo root does not see it, and nothing in `internal/` or `cmd/`
imports it. Measurements are **pending**: only a CPU smoke test has run (below).
`RUN.md` has the commands for the real run and `LAPTOP.md` the Windows steps.

## What it measures

One process per engine, model and accelerator (so peak RSS is that row's own). Whole
clips go in as 16 kHz mono `float32` (ADR-0021), and a warm-up decode runs first.

| Metric | How |
|---|---|
| Time from end of audio to final text | Wall time of the `Transcribe` call on a complete clip, median of `-runs` (default 3). The engines are offline, so the whole clip is the "end of audio". |
| Real-time factor | latency / clip length |
| Load time, first-run time | `Load` call; the warm-up decode (includes lazy GPU init) |
| Peak RSS | `VmHWM` (Linux), `PeakWorkingSetSize` (Windows) |
| Peak VRAM | `nvidia-smi` polled every 250 ms (own pid, else growth of total use) or AMD sysfs; none on CPU or iGPU |
| Word error rate | word-level Levenshtein against the LibriSpeech reference, after lower-casing and stripping punctuation (`wer.go`, unit-tested) |
| Go binding overhead | `inner_ms` is the time inside the engine's decode call; `latency_ms - inner_ms` is the Go wrapper work (stream setup, copying the samples, reading text back). Cross-check by running the `whispercli` engine, which reports whisper.cpp's own timing, against the cgo binding on the same model |

Engines behind one interface (`engine.go`):

| Engine | Code | Models | Accelerators |
|---|---|---|---|
| `sherpa` | `engine_sherpa.go` (tag `sherpa`), the official `sherpa-onnx-go` v2.0.3 | Whisper tiny.en / base.en / small.en / turbo, Parakeet TDT 0.6B v2 | cpu, cuda (GPU libs swapped in), directml (Windows) |
| `whispercpp` | `engine_whispercpp.go` (tag `whispercpp`, plus `wcuda` or `wvulkan`), whisper.cpp `bindings/go` | Whisper ggml: tiny.en / base.en / small.en / turbo / turbo q5_0 | whatever libwhisper was built for: cpu, cuda or vulkan |
| `whispercli` | `engine_whispercli.go`, no cgo | the same ggml models, through a prebuilt `whisper-cli` | whatever that binary has |

`stt-bench -detect` prints the CPU, GPUs, CUDA libraries, Vulkan loader and the
accelerators each compiled-in engine offers. It reads driver and file-system information
only and starts no GPU work.

## Layout

```
main.go summary.go engine*.go sys*.go wav.go wer.go   the harness
accel_*.go                                            which backend a whisper.cpp binary was built for
clips.tsv CLIPS-LICENSE.txt                           the 6 clip ids with exact transcripts
scripts/fetch-clips.sh      LibriSpeech test-clean -> clips/ (gitignored)
scripts/fetch-models.sh     models + models.lock checksums -> ~/.cache/footman-spike/models
scripts/build.sh            sherpa, whispercpp-{cpu,cuda,vulkan}, nocgo
scripts/fetch-sherpa-gpu.sh the CUDA onnxruntime libs the Go binding does not ship
scripts/run-matrix.sh       the whole matrix, writes results/<label>.{jsonl,md}
scripts/package-windows.sh  cross-compile + zip for the laptop
scripts/laptop-run.ps1     what runs on the laptop (shipped as run.ps1)
```

## Smoke test (2026-11-24, CPU only, 4 threads pinned with `taskset -c 0-3`)

12-core desktop CPU, Whisper tiny.en, `05s-1580-141084-0011` (5.0 s), 2 timed runs. This only
shows the harness works; it is not a measurement to decide anything with.

| engine | load ms | first ms | latency ms | RTF | WER | peak RSS MB |
|---|--:|--:|--:|--:|--:|--:|
| sherpa-onnx 2.0.3 (int8) | 315 | 292 | 270 | 0.054 | 0% | 281 |
| whisper.cpp 1.9.6 (cpu) | 61 | 266 | 283 | 0.057 | 0% | 161 |

The GPU was not touched: the CPU rows skip the VRAM sampler entirely, and nothing was
built with CUDA or Vulkan.

## Build and cgo complexity (for the engine decision)

| | sherpa-onnx | whisper.cpp |
|---|---|---|
| Go module | `github.com/k2-fsa/sherpa-onnx-go/v2` (tagged, versioned with the C library) plus one per-OS module | `github.com/ggerganov/whisper.cpp/bindings/go`, **untagged**: a pseudo-version pinned to the v1.9.6 commit; the import path still says `ggerganov` though the repo moved to `ggml-org` |
| Native code | **Prebuilt** `.so` / `.dll` ship inside the Go module (onnxruntime 27 MB + sherpa C API 5 MB on Linux) | **You build it**: cmake, a C++ toolchain, and the right env (`C_INCLUDE_PATH`, `LIBRARY_PATH`, rpath) for cgo |
| Build time here | seconds (only the cgo wrapper compiles) | about 1 min for CPU with 4 jobs; CUDA is much longer |
| `go build` needs | gcc | gcc, g++, cmake; nvcc for CUDA; Vulkan headers and glslc for Vulkan |
| GPU choice | at run time: `Provider` string, but the Go module's libs are **CPU only**. CUDA means swapping in the separate `-gpu` release libs via `LD_LIBRARY_PATH`, and the machine needs CUDA and cuDNN 9 installed (the archive does not bundle them) | at **build** time: one libwhisper per backend, so one binary per accelerator. The high-level Go package calls `whisper_init` with default context params and exposes no `use_gpu` or device setting, so device selection is by environment variable |
| Vulkan | not supported | supported (the universal GPU path, ADR-0010) |
| Windows | cross-compiled from Linux with `zig cc`, no Windows machine needed; ships DLLs, and the same ORT has a DirectML provider | cross-compile not tried. CPU: use the official prebuilt `whisper-cli.exe`. Vulkan needs a Windows or MSVC build with the Vulkan SDK; the release zips have no Vulkan build |
| Binary shape | dynamic: the executable plus `libonnxruntime` and `libsherpa-onnx-c-api` beside it | dynamic: plus `libwhisper`, `libggml*` (static linking is possible but the cgo link order for the backend libraries is fiddly) |
| Distribution to users | needs the right CUDA/cuDNN for a GPU build; CPU is easy | needs the right Vulkan loader or CUDA runtime for GPU builds; ggml libs are small (3 MB for CPU) |

Neither binding has a pure-Go fallback; the `nocgo` build in this spike is the harness
without either engine, which is what the "cloud speech only" build in ADR-0010 looks like.

Notes on the smoke output: sherpa-onnx prints "Only waves less than 30 seconds are
supported" for the two ~29 s clips because it adds tail padding before truncating to
Whisper's 30 s window; the full transcript still comes back (checked on the 29.6 s clip),
so the warning is harmless. Whisper tiny.en has a visible WER on the longer clips; that
is the model, and the real runs use base, small and turbo.
