#!/usr/bin/env bash
# Start an isolated, headless KWin session for the nested input tests
# (input_nested_test.go, build tag "integration"). Adapted from
# spikes/wayland-input/nested/run.sh.
#
#   testdata/nested-kwin.sh ROOT
#
# Everything the session writes goes under ROOT, which should be a short path
# (Unix socket paths are limited to 108 bytes):
#   - kwin_wayland --virtual draws into memory and has no real input devices,
#     so nothing typed in it reaches the desktop, and nothing typed on the
#     desktop reaches it;
#   - a private D-Bus session bus (dbus-run-session), so the portals,
#     kglobalaccel and the permission store are private copies;
#   - private XDG_RUNTIME_DIR, XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_CACHE_HOME
#     and XDG_STATE_HOME, so the desktop's shortcuts, permissions and
#     clipboard are never touched.
# It writes ROOT/env (KEY=VALUE lines) once the session is up, then blocks
# until killed.
set -euo pipefail
ROOT=${1:?usage: nested-kwin.sh ROOT}
# data/applications must exist before the portal starts, or it does not
# notice the .desktop file the tests install there.
mkdir -p "$ROOT"/{config,data/applications,cache,state,run}
chmod 700 "$ROOT/run"
ROOT=$(cd "$ROOT" && pwd)
SOCK=wayland-nested

if [[ -z ${FOOTMAN_NESTED_INNER:-} ]]; then
  export FOOTMAN_NESTED_INNER=1
  # WAYLAND_DISPLAY names the nested socket from the start, so services the
  # private bus starts early (KWin asks for the portal while it starts) find
  # it rather than failing; it is relative to the private XDG_RUNTIME_DIR, so
  # nothing can reach the desktop's compositor.
  exec env -u DISPLAY -u DBUS_SESSION_BUS_ADDRESS -u SESSION_MANAGER \
    WAYLAND_DISPLAY="$SOCK" XDG_RUNTIME_DIR="$ROOT/run" \
    XDG_CONFIG_HOME="$ROOT/config" XDG_DATA_HOME="$ROOT/data" \
    XDG_CACHE_HOME="$ROOT/cache" XDG_STATE_HOME="$ROOT/state" \
    XDG_CURRENT_DESKTOP=KDE XDG_SESSION_TYPE=wayland KDE_FULL_SESSION=true \
    KDE_SESSION_VERSION=6 QT_QPA_PLATFORM=wayland \
    dbus-run-session -- "$0" "$ROOT"
fi

# Inside the private bus from here on.
env -u WAYLAND_DISPLAY kwin_wayland --virtual --width 1600 --height 1000 --socket "$SOCK" >"$ROOT/kwin.log" 2>&1 &
KWIN=$!
trap 'kill $KWIN 2>/dev/null || true' EXIT TERM INT
for _ in $(seq 150); do
  [[ -S $XDG_RUNTIME_DIR/$SOCK ]] && break
  sleep 0.1
done
[[ -S $XDG_RUNTIME_DIR/$SOCK ]] || { echo "kwin_wayland did not start; see $ROOT/kwin.log" >&2; exit 1; }
export WAYLAND_DISPLAY=$SOCK
dbus-update-activation-environment WAYLAND_DISPLAY XDG_RUNTIME_DIR XDG_CONFIG_HOME XDG_DATA_HOME \
  XDG_CACHE_HOME XDG_STATE_HOME XDG_CURRENT_DESKTOP XDG_SESSION_TYPE KDE_FULL_SESSION \
  KDE_SESSION_VERSION QT_QPA_PLATFORM
{
  echo "DBUS_SESSION_BUS_ADDRESS=$DBUS_SESSION_BUS_ADDRESS"
  echo "WAYLAND_DISPLAY=$SOCK"
  echo "XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR"
  echo "XDG_CONFIG_HOME=$XDG_CONFIG_HOME"
  echo "XDG_DATA_HOME=$XDG_DATA_HOME"
  echo "XDG_CACHE_HOME=$XDG_CACHE_HOME"
  echo "XDG_STATE_HOME=$XDG_STATE_HOME"
  echo "XDG_CURRENT_DESKTOP=KDE"
  echo "XDG_SESSION_TYPE=wayland"
  echo "KDE_FULL_SESSION=true"
  echo "KDE_SESSION_VERSION=6"
  echo "QT_QPA_PLATFORM=wayland"
} >"$ROOT/env.tmp"
mv "$ROOT/env.tmp" "$ROOT/env"
wait $KWIN
