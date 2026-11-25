#!/usr/bin/env bash
# OWNER LIVE TEST: opens the default microphone and prints detections.
# NOT run by the automated spike (the mic was never opened).
#   ./live_test.sh sherpa [thr] [boost]   sherpa-onnx KWS: alfred / jarvis / hey footman, then "alfred, stop everything"
#   ./live_test.sh oww    [thr]           openWakeWord:   alexa / hey jarvis / hey mycroft
# Ctrl-C to stop. Nothing is written to disk; audio goes straight from parec into the detector.
set -euo pipefail
cd "$(dirname "$0")"
[ -x data/wakeword ] || go build -o data/wakeword .
rec() { parec --format=float32le --rate=16000 --channels=1 --latency-msec=50; }
case "${1:-sherpa}" in
  sherpa) rec | data/wakeword listen -thr "${2:-0.1}" -boost "${3:-1}" ;;
  oww)    rec | .venv/bin/python py/oww_scores.py --live --live-thr "${2:-0.5}" 2>/dev/null ;;
  *) echo "usage: $0 sherpa|oww [thr] [boost]"; exit 2 ;;
esac
