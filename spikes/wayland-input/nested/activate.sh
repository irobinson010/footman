#!/usr/bin/env bash
# Inside the nested session: print the active window, optionally activate the
# first window whose caption or class contains PATTERN (KWin scripting over D-Bus).
#   activate.sh ROOT [PATTERN]
# Output lands in ROOT/kwin.log ("js:" lines).
set -euo pipefail
ROOT=$1 PAT=${2:-}
js=$(mktemp --suffix=.js)
cat >"$js" <<EOF
const pat = "$PAT";
if (pat) {
  for (const w of workspace.windowList()) {
    if ((w.caption.indexOf(pat) >= 0 || w.resourceClass.indexOf(pat) >= 0) && w.normalWindow) { workspace.activeWindow = w; break; }
  }
}
for (const w of workspace.windowList()) {
  if (w.normalWindow || w.dialog) print("window:", w.caption, "| class:", w.resourceClass, "| active:", w === workspace.activeWindow);
}
EOF
HERE=$(cd "$(dirname "$0")" && pwd)
"$HERE/in.sh" "$ROOT" bash -c "
  id=\$(busctl --user call org.kde.KWin /Scripting org.kde.kwin.Scripting loadScript ss '$js' footman-activate-\$RANDOM | awk '{print \$2}')
  busctl --user call org.kde.KWin /Scripting/Script\$id org.kde.kwin.Script run >/dev/null
  sleep 0.3
  busctl --user call org.kde.KWin /Scripting/Script\$id org.kde.kwin.Script stop >/dev/null || true
"
sleep 0.2
grep 'js: window:' "$ROOT/kwin.log" | tail -n 8
rm -f "$js"
