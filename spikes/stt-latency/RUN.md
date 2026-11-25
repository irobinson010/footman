# Running the full S0-07 measurement (workstation: RTX 4080, Ryzen 9 7900X)

The harness was smoke-tested on CPU only (see README.md). Nothing here has run on
CUDA or Vulkan yet. Do these steps when the GPU is free. Run everything from
`spikes/stt-latency/`.

Expected total: about 20 min of setup and builds, about 15 min of GPU rows and about
30 min of CPU rows, so roughly **1 to 1.25 hours**, most of it unattended. These are
estimates from the smoke test (tiny.en on 4 threads: RTF 0.05) and from published
whisper.cpp numbers, not measurements; the CPU turbo rows dominate.

## 0. Prerequisites (one-off, needs sudo)

| Need | For | Check | Install (Ubuntu / Kubuntu) |
|---|---|---|---|
| gcc, cmake, ninja or make | all cgo builds | `cmake --version` | `sudo apt install build-essential cmake` |
| ffmpeg | clips | `ffmpeg -version` | `sudo apt install ffmpeg` |
| CUDA toolkit 12.x or 13.x (nvcc) | whisper.cpp CUDA build | `nvcc --version` | `sudo apt install nvidia-cuda-toolkit`, or NVIDIA's repo for a newer one |
| cuDNN 9 | sherpa-onnx CUDA | `ldconfig -p \| grep cudnn` | `sudo apt install libcudnn9-cuda-12` (or `-13`) |
| Vulkan headers, loader, glslc | whisper.cpp Vulkan build | `glslc --version` | `sudo apt install libvulkan-dev glslc vulkan-tools` |

On 2026-11-24 this machine had none of nvcc, cudart, cublas, cudnn, the Vulkan
headers or glslc (it has the NVIDIA 595 driver, `libvulkan1` and `mesa-vulkan-drivers`).
The sherpa GPU archive matching your CUDA major version is picked in step 2.

## 1. Clips and models (network only; skip what exists)

```bash
scripts/fetch-clips.sh               # 6 LibriSpeech clips, about 350 MB download once
scripts/fetch-models.sh              # about 3.5 GB into ~/.cache/footman-spike/models, sha256-checked
```

Models: Whisper base.en, small.en, turbo (large-v3-turbo) and turbo q5_0 (ggml only) for
whisper.cpp; base.en, small.en and turbo for sherpa-onnx; Parakeet TDT 0.6B v2 int8
(English transducer, sherpa only). They were already fetched on 2026-11-24.

## 2. Build the binaries

```bash
scripts/build.sh nocgo               # pure Go: summary tool and whispercli engine
scripts/build.sh sherpa              # cgo, prebuilt libs from the Go module (seconds)
scripts/build.sh whispercpp-cpu      # cmake + cgo (about 1 min on 4 jobs)
scripts/build.sh whispercpp-cuda     # CUDA kernels compile slowly: 10 to 20 min on 4 jobs
scripts/build.sh whispercpp-vulkan   # shader generation: a few minutes
scripts/fetch-sherpa-gpu.sh 12       # or 13; match your CUDA toolkit. Puts the GPU onnxruntime in ~/.cache/footman-spike/sherpa-gpu/lib
```

`JOBS=12 scripts/build.sh ...` is fine once the GPU is free and the CPU is not being
used by something else. The default is 4.

Each whisper.cpp accelerator is its own binary because the backend is fixed when
libwhisper is built. sherpa-onnx picks CPU or CUDA at run time, depending on which
`libonnxruntime.so` the loader finds first (`LD_LIBRARY_PATH`).

## 3. Check what the machine reports (no GPU work)

```bash
bin/stt-bench-sherpa -detect
```

Look at `GPUs`, `NvidiaSMI`, `CUDALibs` and `VulkanLoader`. For Vulkan on this
machine there may be several devices (the 4080, the 7900X's tiny iGPU, a CPU
`llvmpipe`). whisper.cpp takes device 0 unless told otherwise, so find the
NVIDIA index with `vulkaninfo --summary` and export it: `export VK_DEVICE=<index>`
(run-matrix.sh turns that into `GGML_VK_VISIBLE_DEVICES`).

## 4. Idle GPU, then run

The VRAM numbers are only clean on an otherwise idle GPU (`nvidia-smi` should show
almost nothing in use, with no render job). Close the browser and anything else
heavy too.

```bash
LABEL=workstation scripts/run-matrix.sh gpu    # CUDA + Vulkan rows, about 15 min
LABEL=workstation scripts/run-matrix.sh cpu    # CPU rows (12 threads), about 30 min
```

`all` does both. Rows missing a binary or a model are skipped with a message, and a
failing row does not stop the run. Results:

- `results/workstation.jsonl`: one JSON line per clip per row
- `results/workstation.md`: the summary table

To add the ZenBook numbers to the table:

```bash
bin/stt-bench-nocgo -summarize results/workstation.jsonl results/zenbook.jsonl -md results/all.md
```

## 5. Sanity checks before trusting a GPU row

- **Did it really use the GPU?** A CUDA or Vulkan row with `peak_vram_mb` of 0 (shown
  as `-` in the table) fell back to CPU. sherpa-onnx falls back silently when the
  CUDA provider cannot load; check its stderr for `Fallback to cpu`, or that
  cuDNN 9 is installed. For Vulkan, the whisper.cpp log lines at start-up name
  the device it picked.
- **CPU rows on a GPU box.** The CPU rows use the `cpu` whisper.cpp build and
  sherpa's CPU libs, so they never touch the GPU.
- **Noise.** Each clip is timed `RUNS` times (default 3) after a warm-up; the table
  reports the median. Re-run with `RUNS=7` if rows look noisy.

## 6. What to do with the numbers

Write `REPORT.md` (the issue's done-when): per engine, model and accelerator the
table above, plus the cgo build notes in README.md, then the recommendation for the
default engine, the model per hardware tier, and the accelerator detection design
(update ADR-0010). Do not commit `results/` raw files unless asked; paste the
summary table into the report.
