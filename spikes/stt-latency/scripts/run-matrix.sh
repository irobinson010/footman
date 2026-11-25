#!/usr/bin/env bash
# Run the whole engine x model x accelerator matrix for one machine.
#
#   scripts/run-matrix.sh cpu      CPU rows only (safe while the GPU is busy)
#   scripts/run-matrix.sh gpu      CUDA and Vulkan rows only (needs an idle GPU)
#   scripts/run-matrix.sh all
#
# Every row is its own process (so peak RSS is that row's), appended to
# results/$LABEL.jsonl. A row whose binary or model is missing is skipped, and a
# failing row is reported and the run carries on. Summary: results/$LABEL.md.
#
# Environment:
#   LABEL         machine label stored in the results (default: workstation)
#   CPU_THREADS   threads for CPU rows (default 12, the 7900X's cores)
#   GPU_THREADS   threads for GPU rows (default 4; the GPU does the work)
#   RUNS          timed runs per clip (default 3)
#   MODELS        space separated model ids (default: all five main ones)
#   VK_DEVICE     index for GGML_VK_VISIBLE_DEVICES (pick the NVIDIA GPU, not the iGPU)
#   SHERPA_GPU_LIB  dir with the sherpa-onnx GPU libs (scripts/fetch-sherpa-gpu.sh)
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
cd "$root"
mode=${1:-all}
label=${LABEL:-workstation}
cpu_threads=${CPU_THREADS:-12}
gpu_threads=${GPU_THREADS:-4}
runs=${RUNS:-3}
models=${MODELS:-"whisper-base.en whisper-small.en whisper-turbo parakeet-tdt-0.6b-v2 whisper-turbo-q5"}
gpu_lib=${SHERPA_GPU_LIB:-$HOME/.cache/footman-spike/sherpa-gpu/lib}
out=results/$label.jsonl
mkdir -p results

row() { # row ENGINE-BINARY ENGINE MODEL ACCEL THREADS [extra env as VAR=val ...]
  local bin=$1 engine=$2 model=$3 accel=$4 threads=$5; shift 5
  [[ -x bin/$bin ]] || { echo "skip $engine/$model/$accel: bin/$bin not built"; return; }
  echo "=== $engine  $model  $accel  ($threads threads)"
  env "$@" "bin/$bin" -engine "$engine" -model "$model" -accel "$accel" -threads "$threads" \
      -runs "$runs" -label "$label" -out "$out" 2> >(grep -v '^\(whisper_\|ggml_\|whisper_init\|$\)' >&2) \
    || echo "!! row failed: $engine/$model/$accel"
}

for m in $models; do
  if [[ $mode == cpu || $mode == all ]]; then
    case $m in
      parakeet-tdt-0.6b-v2) row stt-bench-sherpa sherpa "$m" cpu "$cpu_threads" ;;
      whisper-turbo-q5)     row stt-bench-whispercpp-cpu whispercpp "$m" cpu "$cpu_threads" ;;
      *) row stt-bench-sherpa sherpa "$m" cpu "$cpu_threads"
         row stt-bench-whispercpp-cpu whispercpp "$m" cpu "$cpu_threads" ;;
    esac
  fi
  if [[ $mode == gpu || $mode == all ]]; then
    case $m in
      parakeet-tdt-0.6b-v2) row stt-bench-sherpa sherpa "$m" cuda "$gpu_threads" LD_LIBRARY_PATH="$gpu_lib" ;;
      whisper-turbo-q5)
         row stt-bench-whispercpp-cuda whispercpp "$m" cuda "$gpu_threads"
         row stt-bench-whispercpp-vulkan whispercpp "$m" vulkan "$gpu_threads" ${VK_DEVICE:+GGML_VK_VISIBLE_DEVICES=$VK_DEVICE} ;;
      *) row stt-bench-sherpa sherpa "$m" cuda "$gpu_threads" LD_LIBRARY_PATH="$gpu_lib"
         row stt-bench-whispercpp-cuda whispercpp "$m" cuda "$gpu_threads"
         row stt-bench-whispercpp-vulkan whispercpp "$m" vulkan "$gpu_threads" ${VK_DEVICE:+GGML_VK_VISIBLE_DEVICES=$VK_DEVICE} ;;
    esac
  fi
done

bin/stt-bench-nocgo -summarize "$out" -md "results/$label.md" && echo "wrote $out and results/$label.md"
