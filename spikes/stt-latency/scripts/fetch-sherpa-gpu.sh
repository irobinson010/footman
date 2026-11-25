#!/usr/bin/env bash
# Fetch the sherpa-onnx CUDA (onnxruntime GPU) libraries the Go binding does not
# ship, into ~/.cache/footman-spike/sherpa-gpu/lib. Use them by running the
# sherpa binary with LD_LIBRARY_PATH pointing there (run-matrix.sh does this).
#
#   scripts/fetch-sherpa-gpu.sh 12     CUDA 12.x + cuDNN 9   (default)
#   scripts/fetch-sherpa-gpu.sh 13     CUDA 13.x + cuDNN 9
#
# These libraries do NOT bundle the CUDA runtime or cuDNN; the machine needs
# them installed (RUN.md, prerequisites).
set -euo pipefail
cache=${FOOTMAN_SPIKE_CACHE:-$HOME/.cache/footman-spike}
v=${1:-12}
case $v in
  12) f=sherpa-onnx-v2.0.3-cuda-12.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu.tar.bz2
      sha=240afaa2788151d03be43da257c90c95400f298b8bc81e6934534cb97a25b483 ;;
  13) f=sherpa-onnx-v2.0.3-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu.tar.bz2
      sha=2518e9d013436fcde8468c2e05f6d6e87e489b2d81de86bb57c6b2de7798bd55 ;;
  *) echo "usage: $0 [12|13]" >&2; exit 2 ;;
esac
mkdir -p "$cache/dl"
file=$cache/dl/$f
if [[ ! -f $file ]] || ! echo "$sha  $file" | sha256sum -c --status; then
  curl -fsSL --retry 3 -o "$file.part" "https://github.com/k2-fsa/sherpa-onnx/releases/download/v2.0.3/$f"
  mv "$file.part" "$file"
  echo "$sha  $file" | sha256sum -c - || { rm -f "$file"; echo "CHECKSUM MISMATCH" >&2; exit 1; }
fi
rm -rf "$cache/sherpa-gpu"; mkdir -p "$cache/sherpa-gpu"
tar -xjf "$file" -C "$cache/sherpa-gpu" --strip-components=1 --wildcards '*/lib/*'
echo "sherpa GPU libs in $cache/sherpa-gpu/lib"
