#!/usr/bin/env bash
# Run a command inside the nested session started by run.sh.
#   nested/in.sh ROOT cmd [args...]
set -euo pipefail
ROOT=${1:?usage: in.sh ROOT cmd...}
shift
# shellcheck disable=SC1091
. "$ROOT/env"
exec "$@"
