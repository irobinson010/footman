#!/usr/bin/env bash
# Table -> model folder -> offline renders through Footman's own engine -> measurements.
#
#   finish.sh TABLE.pt TAG
#
# Writes $ROOT/models/ashford-kokoro-TAG/ (the Kokoro model folder plus speaker bm_ashford; point
# Footman's [voice] model at it with speaker "bm_ashford") and $ROOT/renders/TAG/ (10 WAVs).
# Prints RTF/RAM (Go test, CPU), Whisper WER, UTMOS and speaker similarity.
# CPU timings are only clean when nothing else is running.
set -euo pipefail
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
ROOT=${ROOT:-$HOME/PersonalProjects/footman-data/kokoro-train}
DATA=${DATA:-$HOME/PersonalProjects/footman-data}
PY=$ROOT/.venv/bin/python
TABLE=$1 TAG=$2
OUT=$ROOT/models/ashford-kokoro-$TAG
THREADS=${FOOTMAN_TTS_THREADS:-8}
K=$REPO/spikes/tts-bakeoff/kokoro

"$PY" "$K/export_voice.py" "$TABLE" "$OUT" | tail -1
echo "== Footman engine, CPU, $THREADS threads"
(cd "$REPO" && FOOTMAN_TTS_THREADS=$THREADS FOOTMAN_RENDER_ENGINE=kokoro FOOTMAN_RENDER_SPEAKER=bm_ashford \
  FOOTMAN_RENDER_OUT="$ROOT/renders/$TAG" FOOTMAN_RENDER_MODEL="$OUT" FOOTMAN_RENDER_LEXICON_DIR="$DATA/models" \
  FOOTMAN_RENDER_SENTENCES="$REPO/spikes/tts-bakeoff/piper/sentences.txt" \
  go test -tags native -count=1 -run TestRenderSentences -v ./internal/speech/sherpa 2>&1 | grep -E "load |FAIL|panic" | cut -c1-220)
echo "== Whisper base.en"
"$PY" "$REPO/spikes/tts-bakeoff/piper/asr_check.py" "$ROOT/renders/$TAG" 2>&1 | tail -1
echo "== UTMOS"
P=$DATA/ashford-piper-train
(cd "$P" && PYTHONPATH=$P/piper1-gpl/src .venv/bin/python "$REPO/spikes/tts-bakeoff/piper/mos_check.py" "$ROOT/renders/$TAG" 2>&1 | tail -1)
echo "== speaker similarity"
"$PY" "$K/spk_sim.py" "$DATA/ashford-dataset/ref/ashford.wav" "$DATA/ashford-dataset/out/voxcpm2" "$ROOT/spk/spkrec-ecapa-voxceleb" \
  "$ROOT/renders/$TAG" 2>&1 | grep -E "^dataset|^/home"
