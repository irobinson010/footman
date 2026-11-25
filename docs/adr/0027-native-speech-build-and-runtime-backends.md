# 0027. Native speech build: one binary, whisper.cpp backends loaded at run time

- Status: Accepted
- Date: 2026-11-25
- Amends: 0010, 0024, 0025

## Context
S1-06 puts the ADR-0024 engines (whisper.cpp, sherpa-onnx Parakeet and Silero VAD) into the product. The spike (S0-07) left four questions open:

- It used whisper.cpp's Go bindings, which fix the backend when libwhisper is built, so CPU, CUDA and Vulkan were three binaries. ADR-0010 wants the accelerator picked at run time. The bindings are also untagged and expose no `use_gpu` or device setting.
- Both engines need cgo and native libraries, but `go test ./...` and the cross builds must stay pure Go and fast (CONTRIBUTING).
- The default engine was `whisper-local`, a placeholder, and nothing chose the ADR-0024 row.
- Vulkan's first run compiles shaders for 8–30 s, and ggml-vulkan keeps no pipeline cache of its own.

## Decision
- **`native` build tag.** The engines live in `internal/speech/whispercpp` and `internal/speech/sherpa`. Their cgo files build only with `-tags native`; options, defaults and helpers are plain Go and tested everywhere. Without the tag no engine registers, and the daemon refuses to start unless run with `--dev-fakes` (ADR-0025). `make native` builds the native binary.
- **whisper.cpp through a small cgo binding of our own** (about 300 lines against `whisper.h` and `ggml-backend.h`), not the upstream Go bindings. It sets `use_gpu`, the GPU device and an abort callback (so cancelling a context stops a transcription), and reads per-token probabilities for `Transcript.Confidence`.
- **One binary, backends loaded at run time.** `scripts/native/whispercpp.sh` builds whisper.cpp 1.9.6 (checksum-pinned) with `GGML_BACKEND_DL`, so each ggml backend is a plugin: `lib/backends/{cpu,vulkan,cuda}/`, next to `libwhisper`. The engine finds that folder from where `libwhisper` was loaded (`dladdr`), loads the CPU backend and the one the accelerator names, and nothing else. The CPU path therefore never initializes a GPU, and adding CUDA is shipping one more file. `GGML_NATIVE` cannot be combined with `GGML_BACKEND_DL`, so releases build every CPU variant (`GGML_CPU_ALL_VARIANTS`) and ggml loads the best one for the machine. On a machine with both a discrete and an integrated GPU, the engine picks the first discrete GPU unless `device` says otherwise.
- **Registry names:** `whispercpp` and `sherpa-parakeet` (speech-to-text), `silero` (VAD). The wake-word spotter will join `internal/speech/sherpa` and share its wrapper.
- **`speech.stt.engine = "auto"` is the default** (replacing `whisper-local`), and so are `model` and `accelerator`. At every start the daemon detects the hardware from files only (sysfs, `/proc`, the Vulkan ICD manifests, the Vulkan and CUDA loader libraries; no GPU work) and applies the ADR-0024 table within the engines and backends this build has (`internal/speech/accel`). Naming an engine, model or accelerator fixes that part and the rest is still chosen. CUDA is used when asked for, or when there is no Vulkan. Setup's short benchmark (ADR-0010) is not done yet; detection alone picks the row.
- **Warm-up at load, Vulkan shader cache in the cache folder.** whisper.cpp transcribes a second of quiet noise at load, so the daemon is ready only once the GPU shaders are compiled (on the CPU, the first run is also several times slower than later ones). Before the Vulkan backend loads, it points the drivers' shader caches at `<cache>/vulkan` (`__GL_SHADER_DISK_CACHE_PATH` for NVIDIA, `MESA_SHADER_CACHE_DIR` for Mesa), unless the user set them. The folder is recorded in the install receipt (cache group). Measured on the RTX 4080: warm-up 28 s with an empty cache, 0.1 s after.
- **Models:** `internal/speech/models` is a catalog with pinned SHA-256s and sizes. `footman models fetch` downloads into `<data>/models`, verifies, unpacks archives (refusing paths outside the folder, links, and oversize content) and only then moves the model into place, so a model that is present is complete. Each model is recorded as a `model` entry, and the folder as a `dir` entry, in the models group (ADR-0023), before the download starts; a folder given with `--dir` (tests, CI) is not recorded. With no IDs it fetches the default model for this machine and the VAD model. The daemon never downloads; a missing model is an error naming the fetch command.
- **Silero VAD defaults:** threshold 0.5, segment ends after 0.5 s of silence (the wake-word spike used 0.25 s, which splits dictation at short pauses), 30 s maximum.
- **`speech.transcribe {path, vad}`** joins the local API (ADR-0025): it runs a WAV file through the daemon's own VAD and speech-to-text, outside the pipeline, and returns the text, the segments and the time each step took. `footman transcribe FILE.wav` uses it. It is how the engines are checked end to end, by hand and in CI.
- **CI:** a `native` job builds the whisper.cpp CPU library (portable `CPU_VARIANTS=generic`, cached by the script's hash), fetches whisper tiny.en and Silero with `footman models fetch`, and runs the native tests, including the daemon transcribing a LibriSpeech test-clean clip (CC-BY-4.0, in `testdata/speech`).

### How a release ships it (Linux)
```
bin/footman                       built with -tags native, rpath $ORIGIN/../lib/footman
lib/footman/libwhisper.so.1, libggml.so.1, libggml-base.so.1
lib/footman/libsherpa-onnx-c-api.so, libonnxruntime.so   (from the sherpa-onnx Go module)
lib/footman/backends/cpu/libggml-cpu-*.so                 (all CPU variants)
lib/footman/backends/vulkan/libggml-vulkan.so
lib/footman/backends/cuda/libggml-cuda.so                 (optional extra download)
```
The CUDA backend links the CUDA runtime and cuBLAS, so it is an optional download for NVIDIA users rather than part of the default archive. Windows and macOS follow the same layout with their own library names once those platforms land.

## Consequences
- One Linux build serves CPU, Vulkan and CUDA machines, and `footman models default` shows what "auto" picks and why.
- The daemon takes as long to start as the model and warm-up take: about 0.6 s for whisper turbo-q5 on Vulkan with a warm shader cache (30 s on the very first start), about 4.5 s for Parakeet on the CPU.
- Contributors need cmake and a C++ compiler only for the native build; `go test ./...` is unchanged.
- `golangci-lint` in CI checks only the pure-Go files; the native job runs `go vet -tags native`.
- Follow-ups (setup, the benchmark, `speech.vad.min_silence` and idle unloading are done in ADR-0028): setup runs `models fetch` and the warm-up and benchmarks the candidate rows; sherpa-onnx's GPU runtime (ADR-0024) stays unshipped; the integrated-GPU results may move weak iGPUs (such as the 2-CU Raphael iGPU) to the CPU rows; engines unload after `speech.unload_after`; Windows and macOS packaging.
