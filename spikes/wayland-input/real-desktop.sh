#!/usr/bin/env bash
# Helper for the human tests in REPORT.md ("Steps for the user"). Each typing
# command counts down so you can click into the target app first.
#
#   ./real-desktop.sh build                 build everything into bin/
#   ./real-desktop.sh authorise             install the fake_input .desktop files into ~/.local/share/applications
#   ./real-desktop.sh type   METHOD SAMPLE  METHOD = fake | portal | eis | paste-si | paste-csv | paste-cv
#                                           SAMPLE = ascii | unicode
#   ./real-desktop.sh cleanup               remove everything this spike added to your session
set -euo pipefail
cd "$(dirname "$0")"
HERE=$PWD
TOK=${XDG_CACHE_HOME:-$HOME/.cache}/footman-spike

countdown() { for i in 5 4 3 2 1; do printf '\rclick into the target app: %s ' "$i"; sleep 1; done; echo; }

case ${1:-} in
build)
  mkdir -p bin
  for c in cmd/*/; do go build -o "bin/$(basename "$c")" "./$c"; done
  ls bin ;;
authorise)
  for b in kwinfake rdportal rdeis gshortcuts; do nested/install-desktop.sh "$HOME/.local/share" "bin/$b"; done
  echo "KWin picks the new .desktop files up after a few seconds; check with: bin/kwinfake -list | grep fake_input" ;;
type)
  m=$2 s=testdata/$3.txt
  mkdir -p "$TOK"
  countdown
  case $m in
  fake) bin/kwinfake -delay 1ms -file "$s" ;;
  portal) bin/rdportal -app org.footman.spike.rdportal -token-file "$TOK/rd.token" -file "$s" ;;
  eis) bin/rdeis -app org.footman.spike.rdeis -token-file "$TOK/eis.token" -file "$s" ;;
  paste-si) nested/paste.sh shift+insert "$s" ;;
  paste-csv) nested/paste.sh ctrl+shift+v "$s" ;;
  paste-cv) nested/paste.sh ctrl+v "$s" ;;
  *) echo "unknown method $m"; exit 2 ;;
  esac ;;
cleanup)
  rm -fv "$HOME"/.local/share/applications/org.footman.spike.*.desktop
  bin/kglobalaccel -unregister || true
  bin/kglobalaccel -action ptt2 -unregister || true
  echo "Portal shortcut bindings: System Settings > Keyboard > Shortcuts, remove 'Footman spike (gshortcuts)'."
  if command -v flatpak >/dev/null; then
    for a in org.footman.spike.rdportal org.footman.spike.rdeis org.footman.spike.gshortcuts; do
      flatpak permission-reset "$a" 2>/dev/null || true
    done
  fi
  rm -rf "$TOK" ;;
*) sed -n '2,10p' "$0"; exit 2 ;;
esac
