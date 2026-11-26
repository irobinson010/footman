#!/usr/bin/env bash
# Downloads the ready-made models used by the spike into data/models (gitignored).
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p data/models && cd data/models
B=https://github.com/k2-fsa/sherpa-onnx/releases/download
for f in kws-models/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01 \
         tts-models/vits-piper-en_US-libritts_r-medium-int8 \
         tts-models/vits-piper-en_US-ryan-medium-int8 \
         tts-models/vits-piper-en_US-amy-medium-int8; do
  n=$(basename "$f")
  [ -d "$n" ] && continue
  curl -sfLO "$B/$f.tar.bz2"
  tar xjf "$n.tar.bz2" && rm "$n.tar.bz2"
done
[ -f silero_vad.onnx ] || curl -sfLO "$B/asr-models/silero_vad.onnx"
# openWakeWord ONNX models ship inside the pip package (see setup.sh).
ls
