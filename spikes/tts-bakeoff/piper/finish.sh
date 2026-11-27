#!/usr/bin/env bash
# Export a checkpoint, render the test sentences offline, and measure it.
#
#   [BASE="en_GB northern_english_male medium, epoch 2999"] finish.sh RUN CKPT TAG
#
# RUN is the training run (runs/RUN/config.json), CKPT the .ckpt to export
# (for example runs/nem/lightning_logs/version_0/checkpoints/last.ckpt) and TAG
# the output name. Writes $ROOT/models/ashford-piper-TAG/ (the model folder to
# point Footman at) with test WAVs in wavs/. Prints:
#   - CPU render times and RTF/RAM through Footman's own engine (Go test)
#   - Whisper word error rate on the rendered sentences
#   - onnxruntime CPU and GPU forward-pass times
# CPU numbers are only clean when training is not running.
set -euo pipefail
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
ROOT=${ROOT:-$HOME/PersonalProjects/footman-data/ashford-piper-train}
MODELS=${MODELS:-$HOME/PersonalProjects/footman-data/models}
RUN=$1 CKPT=$2 TAG=$3
OUT=$ROOT/models/ashford-piper-$TAG
THREADS=${FOOTMAN_TTS_THREADS:-8}

sed "s|BASE|${BASE:-the base checkpoint}|" "$REPO/spikes/tts-bakeoff/piper/MODEL_CARD" > "$ROOT/card-$TAG.txt"
cp "$CKPT" "$ROOT/exports-$TAG.ckpt"
"$ROOT/.venv/bin/python" "$REPO/spikes/tts-bakeoff/piper/export.py" "$ROOT/exports-$TAG.ckpt" "$OUT" \
  --config "$ROOT/runs/$RUN/config.json" --lexicon "$ROOT/data/lexicon-extra.txt" \
  --piper-src "$ROOT/piper1-gpl/src" --card "$ROOT/card-$TAG.txt" 2>&1 | tail -1
rm -f "$ROOT/exports-$TAG.ckpt" "$ROOT/card-$TAG.txt"
ls -l "$OUT"

echo "== Footman engine, CPU, $THREADS threads"
(cd "$REPO" && FOOTMAN_TTS_THREADS=$THREADS FOOTMAN_RENDER_OUT="$OUT/wavs" FOOTMAN_RENDER_MODEL="$OUT" \
  FOOTMAN_RENDER_LEXICON_DIR="$MODELS" FOOTMAN_RENDER_SENTENCES="$REPO/spikes/tts-bakeoff/piper/sentences.txt" \
  go test -tags native -count=1 -run TestRenderSentences -v ./internal/speech/sherpa 2>&1 | grep -E "tts_render_test|FAIL|panic" | cut -c1-220)

echo "== Whisper base.en"
uv run "$REPO/spikes/tts-bakeoff/piper/asr_check.py" "$OUT/wavs" 2>&1 | tail -12 | cut -c1-230

echo "== onnxruntime"
S=$ROOT/.venv/lib/python3.12/site-packages/nvidia
LD_LIBRARY_PATH=$S/cu13/lib:$S/cudnn/lib uv run "$REPO/spikes/tts-bakeoff/piper/bench_ort.py" "$OUT" --gpu 2>&1 | grep -E "ExecutionProvider \{" | cut -c1-300
