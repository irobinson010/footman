#!/usr/bin/env bash
# Clipboard + simulated paste: put FILE on the clipboard (wl-copy, which uses
# the ext_data_control protocol so it needs no focus), press COMBO through
# kwinfake, then restore the previous clipboard text.
#   paste.sh COMBO FILE      (run inside the session, e.g. via in.sh)
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
COMBO=$1 FILE=$2
prev=$(wl-paste --no-newline 2>/dev/null || true)
# wl-copy forks a daemon that serves the selection; keep it off our stdout or a
# caller reading our output waits until the clipboard changes again.
wl-copy --type text/plain\;charset=utf-8 <"$FILE" >/dev/null 2>&1
"$HERE/bin/kwinfake" -combo "$COMBO"
sleep "${RESTORE_AFTER:-0.3}"   # give the app time to read the selection
if [[ -n $prev ]]; then printf "%s" "$prev" | wl-copy >/dev/null 2>&1; else wl-copy --clear; fi
