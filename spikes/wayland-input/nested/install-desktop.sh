#!/usr/bin/env bash
# Write a .desktop file that authorises a binary for KWin's restricted
# org_kde_kwin_fake_input Wayland interface.
#
#   install-desktop.sh DATA_HOME /abs/path/to/binary
#
# KWin matches the canonical path of the client's executable against the first
# word of Exec= in any installed .desktop file (KApplicationTrader), then reads
# X-KDE-Wayland-Interfaces. For the nested tests DATA_HOME is the nested
# session's XDG_DATA_HOME; for the real desktop it would be ~/.local/share.
set -euo pipefail
DATA=${1:?DATA_HOME}
BIN=$(readlink -f "${2:?binary}")
name=$(basename "$BIN")
mkdir -p "$DATA/applications"
cat >"$DATA/applications/org.footman.spike.$name.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Footman spike ($name)
Exec=$BIN
NoDisplay=true
X-KDE-Wayland-Interfaces=org_kde_kwin_fake_input
EOF
echo "wrote $DATA/applications/org.footman.spike.$name.desktop"
