# Contributing

## Workflow

1. Open (or pick) an issue.
2. Create a branch from `main` for it.
3. Open a pull request that says `Closes #N`. Don't commit directly to `main`.

## Decisions

Every decision change gets an ADR in `docs/adr/` (new, or one that supersedes an older ADR) and a matching update to `docs/design.md`, in the same PR.

## Tests

`go test ./...` must stay fast and self-contained: no GPU, network, desktop or API keys. Use fakes at interfaces. Slow or environment-dependent tests go behind build tags: `integration`, `gpu` or `live`, and run only when asked for (for example `go test -tags integration ./...`).

## Spikes

Spike code lives in `spikes/` and is never imported by the rest of the repo.

## Local tooling

`make build`, `make test`, `make lint` (needs [golangci-lint](https://golangci-lint.run/) v2) and `make cross`.

### Native speech engines

The real speech engines (whisper.cpp and sherpa-onnx, ADR-0027) need cgo and are built only with the `native` tag. You need cmake and a C++17 compiler, plus `glslc` and the Vulkan headers (`libvulkan-dev`) for Vulkan, or `nvcc` for CUDA.

```bash
make native                           # whisper.cpp with CPU + Vulkan, then bin/footman -tags native
make native ACCELS="cpu vulkan cuda"  # add the CUDA backend (slow to compile)
make native ACCELS=cpu CPU_VARIANTS=generic   # quickest: what CI builds
bin/footman models default            # what "auto" picks on this machine
bin/footman models fetch              # download that model and the VAD
```

whisper.cpp is built once into `~/.cache/footman-native/` (by `scripts/native/whispercpp.sh`; `eval "$(scripts/native/whispercpp.sh env)"` sets `CGO_CFLAGS` and `CGO_LDFLAGS` for your own `go build -tags native`). The native tests need models: point `FOOTMAN_TEST_MODELS` at a models folder (`footman models --dir DIR fetch whisper-tiny.en silero-vad parakeet-tdt-0.6b-v2`) and run `make test-native`; tests whose model is missing are skipped. `FOOTMAN_TEST_STT`, `FOOTMAN_TEST_WHISPER_MODEL` and `FOOTMAN_TEST_ACCEL` pick what `TestDaemonTranscribesWAV` runs, for example `FOOTMAN_TEST_STT=auto FOOTMAN_TEST_ACCEL=auto` for the GPU path.
