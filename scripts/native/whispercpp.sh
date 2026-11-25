#!/usr/bin/env bash
# Build whisper.cpp for Footman's `native` build (ADR-0027).
#
# One build serves every accelerator: whisper.cpp is built with
# GGML_BACKEND_DL, so the ggml backends (CPU, Vulkan, CUDA) are separate
# plugins that the whispercpp engine loads at run time. The result is laid out
# as
#
#   $PREFIX/include/          whisper.h, ggml*.h          (for cgo)
#   $PREFIX/lib/              libwhisper, libggml, libggml-base
#   $PREFIX/lib/backends/cpu/     libggml-cpu*.so  (one or more CPU variants)
#   $PREFIX/lib/backends/vulkan/  libggml-vulkan.so
#   $PREFIX/lib/backends/cuda/    libggml-cuda.so
#
# and the engine finds backends/ next to the libwhisper it was loaded from.
#
# Usage:
#   scripts/native/whispercpp.sh                 # CPU only
#   ACCELS="cpu vulkan" scripts/native/whispercpp.sh
#   ACCELS="cpu vulkan cuda" scripts/native/whispercpp.sh
#   eval "$(scripts/native/whispercpp.sh env)"   # print the cgo environment
#
# Environment:
#   PREFIX        install prefix (default ~/.cache/footman-native/whisper.cpp-$VERSION-<accels>-<variants>)
#   ACCELS        backends to build: cpu, vulkan, cuda (default "cpu"; cpu is always built)
#   CPU_VARIANTS  all (default: every x86/arm CPU variant, the best picked at
#                 run time; what releases ship) or generic (one portable build,
#                 quicker to compile; used by CI). GGML_NATIVE cannot be
#                 combined with GGML_BACKEND_DL.
#   JOBS          build parallelism (default: number of CPUs)
#   CMAKE_EXTRA   extra cmake flags, e.g. -DCMAKE_CUDA_ARCHITECTURES=89 to
#                 build CUDA kernels for one GPU generation only
#
# Needs cmake, a C++17 compiler; glslc and the Vulkan headers for vulkan; nvcc
# for cuda.
set -euo pipefail

VERSION=1.9.6
SHA256=7ab15dc88022d2425ad94dcffd8abbf1b6d7018089b4e48d6b40780738e0b445
URL=https://github.com/ggml-org/whisper.cpp/archive/refs/tags/v$VERSION.tar.gz

accels=${ACCELS:-cpu}
variants=${CPU_VARIANTS:-all}
jobs=${JOBS:-$(nproc 2>/dev/null || echo 4)}
cache=${FOOTMAN_NATIVE_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/footman-native}
tag=$(echo "cpu $accels" | tr ' ' '\n' | sort -u | grep -v '^$' | paste -sd- -)
prefix=${PREFIX:-$cache/whisper.cpp-$VERSION-$tag-$variants}

if [[ "${1:-}" == env ]]; then
  echo "export CGO_CFLAGS=\"-I$prefix/include\""
  echo "export CGO_LDFLAGS=\"-L$prefix/lib -Wl,-rpath,$prefix/lib\""
  exit 0
fi

if [[ -f "$prefix/.done" ]]; then
  echo "whisper.cpp $VERSION ($tag, $variants) already built in $prefix"
  exit 0
fi

src=$cache/src/whisper.cpp-$VERSION
if [[ ! -d "$src" ]]; then
  mkdir -p "$cache/src"
  tgz=$cache/src/whisper.cpp-$VERSION.tar.gz
  curl -fsSL --retry 3 -o "$tgz.part" "$URL"
  echo "$SHA256  $tgz.part" | sha256sum -c --status || { echo "checksum mismatch for $URL" >&2; rm -f "$tgz.part"; exit 1; }
  mv "$tgz.part" "$tgz"
  tar -xzf "$tgz" -C "$cache/src"
fi

flags=(-DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON -DGGML_BACKEND_DL=ON
  -DWHISPER_BUILD_EXAMPLES=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF
  -DCMAKE_INSTALL_RPATH='$ORIGIN')
case $variants in
  generic) flags+=(-DGGML_NATIVE=OFF) ;;
  all)     flags+=(-DGGML_NATIVE=OFF -DGGML_CPU_ALL_VARIANTS=ON) ;;
  *) echo "CPU_VARIANTS must be all or generic" >&2; exit 2 ;;
esac
for a in $accels; do
  case $a in
    cpu) ;;
    vulkan) flags+=(-DGGML_VULKAN=ON) ;;
    cuda)   flags+=(-DGGML_CUDA=ON) ;;
    *) echo "unknown accelerator $a (cpu, vulkan, cuda)" >&2; exit 2 ;;
  esac
done

build=$cache/build/whisper.cpp-$VERSION-$tag-$variants
command -v ninja >/dev/null && flags+=(-G Ninja)
# shellcheck disable=SC2206 # CMAKE_EXTRA is a list of flags
flags+=(${CMAKE_EXTRA:-})
cmake -S "$src" -B "$build" "${flags[@]}"
cmake --build "$build" --parallel "$jobs"

rm -rf "$prefix"
mkdir -p "$prefix/include" "$prefix/lib/backends"
cp "$src/include/whisper.h" "$src"/ggml/include/*.h "$prefix/include/"
# The core libraries keep their sonames; cgo links against them.
cp -P "$build"/bin/libwhisper.so* "$build"/bin/libggml.so* "$build"/bin/libggml-base.so* "$prefix/lib/"
for b in cpu vulkan cuda; do
  libs=()
  for f in "$build"/bin/libggml-$b.so "$build"/bin/libggml-$b-*.so; do
    [[ -f "$f" ]] && libs+=("$f")
  done
  ((${#libs[@]})) || continue
  mkdir -p "$prefix/lib/backends/$b"
  cp "${libs[@]}" "$prefix/lib/backends/$b/"
done
touch "$prefix/.done"
echo "whisper.cpp $VERSION built in $prefix"
echo "cgo environment: eval \"\$($0 env)\""
