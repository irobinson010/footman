#!/usr/bin/env bash
# Build the Piper training inputs from the S0-09 dataset (S0-10).
#
#   prepare.sh            (run from anywhere; paths below are overridable)
#
# Writes under $ROOT/data:
#   wav22/            kept lines resampled to 22.05 kHz mono 16-bit
#   lexicon-extra.txt espeak-ng IPA for words Footman's lexicon lacks
#   phonemes.psv      "id|normalized text|phonemes", made by Footman's own front end
#   metadata.csv      "id.wav|phonemes" for piper.train --data.phoneme_type text
#
# Prerequisites: the venv from setup.sh, ffmpeg, Go.
set -euo pipefail
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
ROOT=${ROOT:-/home/ian/PersonalProjects/footman-data/ashford-piper-train}
DATA=${DATASET:-/home/ian/PersonalProjects/footman-data/ashford-dataset/out/voxcpm2}
MODELS=${MODELS:-/home/ian/PersonalProjects/footman-data/models}
mkdir -p "$ROOT/data/wav22"

# 1. Kept lines only, resampled from 24 kHz to Piper's 22.05 kHz.
python3 - "$DATA/manifest.jsonl" > "$ROOT/data/keep.txt" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    r = json.loads(line)
    if r["verdict"] == "keep":
        print(r["id"])
EOF
xargs -a "$ROOT/data/keep.txt" -P 12 -I{} \
  ffmpeg -loglevel error -y -i "$DATA/wavs/{}.wav" -ar 22050 -ac 1 -c:a pcm_s16le "$ROOT/data/wav22/{}.wav"

# 2. Words Footman's lexicon lacks (her, or, per, rare names): espeak-ng IPA, shipped
#    beside the voice as lexicon.txt and used here too so training sees what runtime sees.
"$ROOT/.venv/bin/python" "$REPO/spikes/tts-bakeoff/piper/make_extra_lexicon.py" \
  "$ROOT/data/lexicon-extra.txt" "$MODELS" "$ROOT/piper1-gpl/src" \
  Lakshmi Lindqvist Tomasz Kwame Priya Oyelaran Yusuf Petrov Zilla provocateurs unquenchable audacious "that'll"

# 3. Phonemes from Footman's own text front end and G2P (a Go test used as a tool).
(cd "$REPO" && FOOTMAN_PHONEMES_KEEP=1 FOOTMAN_PHONEMES_MODELS="$MODELS" \
  FOOTMAN_PHONEMES_EXTRA="$ROOT/data/lexicon-extra.txt" \
  FOOTMAN_PHONEMES_IN="$DATA/manifest.jsonl" FOOTMAN_PHONEMES_OUT="$ROOT/data/phonemes.psv" \
  go test ./internal/speech/sherpa -run TestDumpPiperPhonemes -v -count=1)
awk -F'|' '{print $1".wav|"$3}' "$ROOT/data/phonemes.psv" > "$ROOT/data/metadata.csv"
wc -l "$ROOT/data/metadata.csv"
