#!/usr/bin/env bash
# One-time setup for the S0-08 wake-word spike. CPU only; nothing here touches the GPU or the mic.
#   1. Python venv (helpers: TTS synthesis, corpus download, openWakeWord scoring)
#   2. models (sherpa KWS, Silero VAD, Piper voices)   -> data/models   (gitignored)
#   3. ~1 h of background audio from CC corpora         -> data/bg      (gitignored)  ~45 min, ~0.5 GB download
#   4. synthetic keyword clips mixed at several SNRs     -> data/pos     (gitignored)  ~10 min
#   5. the Go harness                                    -> data/wakeword
# LIVE_ONLY=1 ./setup.sh   does 1, 2 and 5 only (enough for ./live_test.sh).
set -euo pipefail
cd "$(dirname "$0")"
command -v uv >/dev/null || { echo "need uv (https://docs.astral.sh/uv/)"; exit 1; }
[ -d .venv ] || uv venv --python 3.12 .venv
# sherpa-onnx is pinned to the same version as the Go module: 2.x wheels lost the espeak frontend the Piper voices need.
VIRTUAL_ENV=$PWD/.venv uv pip install sherpa-onnx==1.13.8 openwakeword onnxruntime numpy soundfile scipy \
  sentencepiece click remotezip datasets pandas
bash fetch_models.sh
.venv/bin/python py/make_keywords.py
if [ -z "${LIVE_ONLY:-}" ]; then
  [ -f data/bg/hour.wav ] || nice .venv/bin/python py/fetch_audio.py
  [ -f data/pos/index.json ] || nice .venv/bin/python py/synth.py
fi
go build -o data/wakeword .
