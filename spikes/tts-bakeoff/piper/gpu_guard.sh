#!/usr/bin/env bash
# Stop the training run if anything else takes the GPU (Troupe has priority).
#
#   gpu_guard.sh RUN_PID LOGFILE
#
# Every 30 s: if a compute process other than our venv python or the footman service is on the GPU, or
# a docker container named troupe-S*, tts-* or troupe-tts-* is running, send
# SIGTERM to the training. train.sh resumes from last.ckpt (at most one epoch
# is lost), so re-run it when the GPU is free again.
RUN_PID=$1 LOG=$2
i=0
while kill -0 "$RUN_PID" 2>/dev/null; do
  # Host PIDs differ from ours inside the sandbox, so match by process name:
  # our own venv python and the user's installed footman service are fine.
  other=$(nvidia-smi --query-compute-apps=pid,name --format=csv,noheader 2>/dev/null |
    grep -v -E 'ashford-piper-train/\.venv|/footman$' | cut -d, -f1 | tr '\n' ' ')
  boxes=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^(troupe-S|tts-|troupe-tts)' | tr '\n' ' ')
  if [ -n "$other$boxes" ]; then
    echo "$(date -Is) GPU taken (pids:$other containers: $boxes); stopping training $RUN_PID" >> "$LOG"
    kill -TERM "$RUN_PID"
    exit 0
  fi
  i=$((i+1))
  if [ $((i % 40)) -eq 1 ]; then echo "$(date -Is) ok: $(nvidia-smi --query-gpu=memory.used,utilization.gpu --format=csv,noheader)" >> "$LOG"; fi
  sleep 30
done
