#!/usr/bin/env bash
# The slow sherpa-onnx runs, split over two processes (2 threads total).
#   A: wake-sensitivity sweep on the synthetic clips (stage 2 fixed at boost 1 / thr 0.25)
#   B: stage-2 sensitivity sweep (wake fixed at boost 2 / thr 0.05), then the 1 h false-wake sweep
set -euo pipefail
cd "$(dirname "$0")"
W=data/wakeword
R=data/results
mkdir -p $R
go build -o $W .
S="-thr 0.25,0.1,0.05,0.02,0.01 -boost 1,1,2,4,8"   # wake sensitivity: boost/threshold pairs, least to most sensitive
nice -n 10 $W pos $S > $R/sherpa_pos.md &
A=$!
(
  for s2 in "0.1 2" "0.05 4" "0.02 8"; do
    set -- $s2
    nice -n 10 $W pos -thr 0.05 -boost 2 -s2thr "$1" -s2boost "$2"
  done > $R/sherpa_pos_s2sweep.md
  nice -n 10 $W hour $S -diag=false > $R/sherpa_hour.md
  nice -n 10 $W hour -thr 0.25 -diag=true > $R/sherpa_hour_diag.md
) &
B=$!
wait $A $B
