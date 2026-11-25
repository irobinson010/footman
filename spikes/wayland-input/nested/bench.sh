#!/usr/bin/env bash
# Type each sample into the focused sink inside the nested session and report
# correctness and speed.
#
#   bench.sh ROOT LOG METHOD [samples...]
#
# METHOD is a command that types the file given as its last argument, e.g.
#   "bin/kwinfake -file"   or   "bin/rdportal -mode keysym -file"
# The sink is cleared with Ctrl+A, BackSpace (via kwinfake) before each sample,
# or with Ctrl+L for ttysink (CLEAR=tty), or not at all (CLEAR=none).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
ROOT=$1 LOG=$2 METHOD=$3
shift 3
samples=("$@")
[[ ${#samples[@]} -eq 0 ]] && samples=("$HERE/testdata/ascii.txt" "$HERE/testdata/unicode.txt")
nin() { "$HERE/nested/in.sh" "$ROOT" "$@"; }
for s in "${samples[@]}"; do
  if [[ ${CLEAR:-gui} == tty ]]; then
    nin "$HERE/bin/kwinfake" -combo ctrl+l
  elif [[ ${CLEAR:-gui} != none ]]; then
    nin "$HERE/bin/kwinfake" -combo ctrl+a
    nin "$HERE/bin/kwinfake" -combo backspace
  fi
  sleep 0.5
  start=$(date +%s%3N)
  # shellcheck disable=SC2086
  nin $METHOD "$s" 2>&1 | sed 's/^/  sender: /'
  sleep "${SETTLE:-1.5}"
  printf '%-12s ' "$(basename "$s")"
  "$HERE/bin/analyze" -log "$LOG" -expect-file "$s" -since "$start" || true
done
