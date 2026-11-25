#!/usr/bin/env bash
# Build stt-bench variants into spikes/stt-latency/bin/.
#
#   scripts/build.sh sherpa              cgo, prebuilt libs from the Go module; CPU, and CUDA with the -gpu libs
#   scripts/build.sh whispercpp-cpu      builds libwhisper (cmake) first, then the cgo binary
#   scripts/build.sh whispercpp-cuda     needs the CUDA toolkit (nvcc)
#   scripts/build.sh whispercpp-vulkan   needs libvulkan-dev and glslc (shaderc)
#   scripts/build.sh nocgo               pure Go: whispercli engine + WER/summary tools only
#   scripts/build.sh all-linux           everything above that this machine can build
#
# JOBS (default 4) caps cmake and go build parallelism.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
cache=${FOOTMAN_SPIKE_CACHE:-$HOME/.cache/footman-spike}
jobs=${JOBS:-4}
whisper_tag=v1.9.6   # keep in step with the bindings version in go.mod
src=$cache/src/whisper.cpp-${whisper_tag#v}
mkdir -p "$root/bin" "$cache/src"

fetch_whisper() {
  [[ -d "$src" ]] && return
  echo "fetching whisper.cpp $whisper_tag"
  curl -fsSL "https://github.com/ggml-org/whisper.cpp/archive/refs/tags/$whisper_tag.tar.gz" | tar -xz -C "$cache/src"
}

# build_whisper ACCEL: static or shared libwhisper for the accelerator, then the Go binary.
build_whisper() {
  local accel=$1 flags=() tags=whispercpp bdir
  case $accel in
    cpu) ;;
    cuda)   flags+=(-DGGML_CUDA=ON);   tags+=,wcuda ;;
    vulkan) flags+=(-DGGML_VULKAN=ON); tags+=,wvulkan ;;
    *) echo "unknown accel $accel" >&2; exit 2 ;;
  esac
  fetch_whisper
  bdir=$src/build-$accel
  cmake -S "$src" -B "$bdir" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON \
    -DWHISPER_BUILD_EXAMPLES=OFF -DWHISPER_BUILD_TESTS=OFF -DGGML_NATIVE=ON "${flags[@]}"
  cmake --build "$bdir" --target whisper --parallel "$jobs"
  (cd "$root" && \
    C_INCLUDE_PATH=$src/include:$src/ggml/include LIBRARY_PATH=$bdir/bin \
    CGO_LDFLAGS="-Wl,-rpath,$bdir/bin" \
    go build -p "$jobs" -tags "$tags" -o "bin/stt-bench-whispercpp-$accel" .)
  echo "built bin/stt-bench-whispercpp-$accel (libs in $bdir/bin, rpath set)"
}

build() {
  case $1 in
    sherpa)  (cd "$root" && go build -p "$jobs" -tags sherpa -o bin/stt-bench-sherpa .) ;;
    whispercpp-cpu)    build_whisper cpu ;;
    whispercpp-cuda)   build_whisper cuda ;;
    whispercpp-vulkan) build_whisper vulkan ;;
    nocgo)   (cd "$root" && CGO_ENABLED=0 go build -o bin/stt-bench-nocgo .) ;;
    all-linux) build nocgo; build sherpa; build whispercpp-cpu
               command -v nvcc >/dev/null && build whispercpp-cuda || echo "skip cuda: no nvcc"
               [[ -f /usr/include/vulkan/vulkan.h ]] && command -v glslc >/dev/null && build whispercpp-vulkan || echo "skip vulkan: no vulkan headers or glslc" ;;
    *) echo "unknown target $1" >&2; exit 2 ;;
  esac
}
build "${1:-all-linux}"
