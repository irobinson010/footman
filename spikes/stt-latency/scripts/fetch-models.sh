#!/usr/bin/env bash
# Download the spike's models into ~/.cache/footman-spike/models (override with
# FOOTMAN_SPIKE_CACHE) and verify every file against models.lock.
#
#   scripts/fetch-models.sh            # everything (about 3.5 GB)
#   scripts/fetch-models.sh smoke      # whisper-tiny.en, both engines (about 200 MB)
#   scripts/fetch-models.sh quick      # tiny.en, base.en, small.en (about 1.6 GB)
#   scripts/fetch-models.sh whisper-base.en parakeet-tdt-0.6b-v2   # by id
#
# Network only; no GPU and little CPU (bzip2 extraction).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cache=${FOOTMAN_SPIKE_CACHE:-$HOME/.cache/footman-spike}
models=$cache/models
dl=$cache/dl
mkdir -p "$models/sherpa" "$models/whispercpp" "$dl"

case "${1:-all}" in
  smoke) want="whisper-tiny.en" ;;
  quick) want="whisper-tiny.en whisper-base.en whisper-small.en" ;;
  all)   want=$(awk '!/^#/ && NF {print $1}' "$here/models.lock" | sort -u | tr '\n' ' ') ;;
  *)     want="$*" ;;
esac

# models.lock columns: id  engine  sha256  dest  url   (tab separated)
while IFS=$'\t' read -r id engine sha dest url; do
  [[ -z "${id:-}" || "$id" == \#* ]] && continue
  [[ " $want " == *" $id "* ]] || continue
  file=$dl/$(basename "$url")
  if [[ ! -f "$file" ]] || ! echo "$sha  $file" | sha256sum -c --status; then
    echo "download $id ($engine): $url"
    curl -fsSL --retry 3 -o "$file.part" "$url"
    mv "$file.part" "$file"
    echo "$sha  $file" | sha256sum -c - || { echo "CHECKSUM MISMATCH for $file" >&2; rm -f "$file"; exit 1; }
  else
    echo "ok       $id ($engine): checksum verified"
  fi
  if [[ "$engine" == sherpa ]]; then
    [[ -d "$models/sherpa/$dest" ]] || { echo "extract  $file"; tar -xjf "$file" -C "$models/sherpa"; }
  else
    cp -n "$file" "$models/whispercpp/$dest" 2>/dev/null || true
  fi
done < "$here/models.lock"
echo "models in $models"
