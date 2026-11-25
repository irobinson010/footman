#!/usr/bin/env bash
# Start an isolated, headless KWin session for input tests.
#
#   nested/run.sh ROOT
#
# ROOT is a scratch directory. Everything the nested session writes (KDE config,
# kglobalshortcutsrc, the portal permission store, caches) goes under ROOT, so
# the real desktop's settings are never touched.
#
# Isolation:
#   - kwin_wayland --virtual: renders to memory, no real screen, no real input
#     devices (no libinput/logind seat), so nothing typed here reaches the
#     user's desktop and nothing typed on the desktop reaches here.
#   - its own D-Bus session bus (dbus-run-session), so the portals, kglobalaccel
#     and KWin's EIS D-Bus API are private copies.
#   - XDG_CONFIG_HOME / XDG_DATA_HOME / XDG_CACHE_HOME / XDG_STATE_HOME under ROOT.
#   - DISPLAY unset; no Xwayland unless XWAYLAND=1.
#
# The script writes ROOT/env, which other scripts source to talk to the session,
# and blocks until killed.
set -euo pipefail
ROOT=${1:?usage: run.sh ROOT}
mkdir -p "$ROOT"/{config,data,cache,state}
ROOT=$(cd "$ROOT" && pwd)
SOCK=${SOCK:-wayland-77}

if [[ -z ${FOOTMAN_NESTED_INNER:-} ]]; then
  export FOOTMAN_NESTED_INNER=1
  exec env -u DISPLAY -u WAYLAND_DISPLAY -u DBUS_SESSION_BUS_ADDRESS \
    XDG_CONFIG_HOME="$ROOT/config" XDG_DATA_HOME="$ROOT/data" \
    XDG_CACHE_HOME="$ROOT/cache" XDG_STATE_HOME="$ROOT/state" \
    XDG_CURRENT_DESKTOP=KDE XDG_SESSION_TYPE=wayland KDE_FULL_SESSION=true \
    KDE_SESSION_VERSION=6 QT_QPA_PLATFORM=wayland SOCK="$SOCK" \
    dbus-run-session -- "$0" "$ROOT"
fi

# Inside the private bus from here on.
xw=()
[[ ${XWAYLAND:-0} == 1 ]] && xw=(--xwayland)
kwin_wayland --virtual --width 1600 --height 1000 --socket "$SOCK" "${xw[@]}" \
  >"$ROOT/kwin.log" 2>&1 &
KWIN=$!
for _ in $(seq 100); do
  [[ -S $XDG_RUNTIME_DIR/$SOCK ]] && break
  sleep 0.1
done
export WAYLAND_DISPLAY=$SOCK
dbus-update-activation-environment WAYLAND_DISPLAY XDG_CONFIG_HOME XDG_DATA_HOME \
  XDG_CACHE_HOME XDG_STATE_HOME XDG_CURRENT_DESKTOP XDG_SESSION_TYPE KDE_FULL_SESSION \
  KDE_SESSION_VERSION QT_QPA_PLATFORM
if [[ ${XWAYLAND:-0} == 1 ]]; then
  # KWin starts Xwayland lazily; the display it reserved is the lock file that
  # holds KWin's pid.
  sleep 1
  for l in /tmp/.X*-lock; do
    if [[ $(tr -d ' \n' <"$l") == "$KWIN" ]]; then DISPLAY=:$(basename "$l" | tr -dc 0-9); fi
  done
fi
{
  echo "export DBUS_SESSION_BUS_ADDRESS='$DBUS_SESSION_BUS_ADDRESS'"
  echo "export WAYLAND_DISPLAY='$SOCK'"
  echo "export XDG_CONFIG_HOME='$ROOT/config' XDG_DATA_HOME='$ROOT/data'"
  echo "export XDG_CACHE_HOME='$ROOT/cache' XDG_STATE_HOME='$ROOT/state'"
  echo "export XDG_CURRENT_DESKTOP=KDE XDG_SESSION_TYPE=wayland KDE_FULL_SESSION=true KDE_SESSION_VERSION=6"
  echo "export QT_QPA_PLATFORM=wayland"
  if [[ -n ${DISPLAY:-} ]]; then echo "export DISPLAY='$DISPLAY'"; else echo "unset DISPLAY"; fi
} >"$ROOT/env"
echo "nested session up: $ROOT/env (kwin pid $KWIN)"
wait $KWIN
