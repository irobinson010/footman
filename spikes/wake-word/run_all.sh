#!/usr/bin/env bash
# Runs every automated measurement and writes the tables to data/results/*.md (gitignored;
# the numbers that matter are copied into REPORT.md). CPU only, one thread per engine,
# at most two engines at a time. Run ./setup.sh first.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p data/results
W=data/wakeword
R=data/results
go build -o $W .

# --- sherpa-onnx KWS: wake sweep, stage-2 sweep, 1 h false-wake sweep (two processes) ---
./run_sherpa.sh

# --- Silero VAD ---
$W vad -wav data/bg/hour.wav -q | tee $R/vad_hour.md
$W vadtest | tee $R/vad_pos.md

# --- openWakeWord (Python helper writes score timelines, Go thresholds them) ---
.venv/bin/python py/oww_scores.py $R/oww_hour.json data/bg/hour.wav 2>&1 | tee $R/oww_hour.log
.venv/bin/python py/oww_scores.py $R/oww_hour_vad.json data/bg/hour.wav --vad 0.5 2>&1 | tee $R/oww_hour_vad.log
python3 -c "import json;print('\n'.join(c['file'] for c in json.load(open('data/pos/index.json'))))" > $R/pos_files.txt
.venv/bin/python py/oww_scores.py $R/oww_pos.json $R/pos_files.txt --dir data 2>&1 | tee $R/oww_pos.log
$W oww-hour -scores $R/oww_hour.json | tee $R/oww_hour.md
$W oww-hour -scores $R/oww_hour_vad.json | tee $R/oww_hour_vad.md
$W oww-pos -scores $R/oww_pos.json | tee $R/oww_pos.md
