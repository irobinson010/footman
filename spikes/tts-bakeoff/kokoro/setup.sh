#!/usr/bin/env bash
# Environment for the Kokoro style-vector fit (S0-10). No sudo, no system packages.
#
#   setup.sh        creates $ROOT/.venv, downloads the Kokoro-82M weights and voices (Apache-2.0)
#                   and copies the ECAPA speaker scorer from Troupe's read-only model folder
#                   (or downloads speechbrain/spkrec-ecapa-voxceleb if that is absent).
set -euo pipefail
ROOT=${ROOT:-$HOME/PersonalProjects/footman-data/kokoro-train}
mkdir -p "$ROOT" && cd "$ROOT"
uv venv -q --python 3.12 .venv
uv pip install -q --python .venv/bin/python torch torchaudio kokoro numpy scipy soundfile librosa \
  faster-whisper onnxruntime onnx speechbrain transformers
.venv/bin/python - <<'EOF'
from huggingface_hub import snapshot_download
snapshot_download("hexgrad/Kokoro-82M", local_dir="base/Kokoro-82M",
                  allow_patterns=["config.json", "kokoro-v1_0.pth", "voices/*.pt"])
EOF
mkdir -p spk
TROUPE=$HOME/PersonalProjects/reading_text_in_real_voice/models/spkrec-ecapa-voxceleb
if [ -d "$TROUPE" ]; then cp -rL "$TROUPE" spk/; else
  .venv/bin/python -c "from huggingface_hub import snapshot_download as d; d('speechbrain/spkrec-ecapa-voxceleb', local_dir='spk/spkrec-ecapa-voxceleb')"
fi
# Phonemes for the kept dataset lines from Footman's own front end (Kokoro alphabet, British lexicon).
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
DATA=${DATASET:-$HOME/PersonalProjects/footman-data/ashford-dataset/out/voxcpm2}
MODELS=${MODELS:-$HOME/PersonalProjects/footman-data/models}
mkdir -p data
(cd "$REPO" && FOOTMAN_PHONEMES_FAMILY=kokoro FOOTMAN_PHONEMES_KEEP=1 FOOTMAN_PHONEMES_MODELS="$MODELS" \
  FOOTMAN_PHONEMES_IN="$DATA/manifest.jsonl" FOOTMAN_PHONEMES_OUT="$ROOT/data/phonemes.psv" \
  go test ./internal/speech/sherpa -run TestDumpPiperPhonemes -v -count=1 | grep -E "wrote|FAIL")
