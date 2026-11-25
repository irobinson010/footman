#!/usr/bin/env bash
# Build spikes/stt-latency/clips/ from LibriSpeech test-clean (CC-BY-4.0,
# https://www.openslr.org/12): six utterances, two each near 5 s, 10 s and 30 s,
# converted to 16 kHz mono WAV with the exact reference transcript beside each.
# The list and transcripts live in clips.tsv (id <TAB> transcript); the audio is
# never committed (clips/ is gitignored).
#
# Needs curl, tar and ffmpeg. Downloads the 346 MB archive once into
# ~/.cache/footman-spike/dl (md5 checked) and extracts only the six files.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
cache=${FOOTMAN_SPIKE_CACHE:-$HOME/.cache/footman-spike}
mkdir -p "$cache/dl" "$root/clips"

tarball=$cache/dl/test-clean.tar.gz
md5=32fa31d27d2e1cad72775fee3f4849a9
if [[ ! -f "$tarball" ]] || [[ "$(md5sum "$tarball" | cut -d' ' -f1)" != "$md5" ]]; then
  curl -fsSL --retry 3 -o "$tarball" https://www.openslr.org/resources/12/test-clean.tar.gz
  [[ "$(md5sum "$tarball" | cut -d' ' -f1)" == "$md5" ]] || { echo "md5 mismatch for $tarball" >&2; exit 1; }
fi

work=$(mktemp -d "$cache/clips-XXXXXX")
trap 'rm -rf "$work"' EXIT
members=()
while IFS=$'\t' read -r id _; do
  [[ -z "$id" ]] && continue
  u=${id#*s-}                       # 1580-141084-0011
  spk=${u%%-*}; rest=${u#*-}; chap=${rest%%-*}
  members+=("LibriSpeech/test-clean/$spk/$chap/$u.flac")
done < "$here/../clips.tsv"
tar -xzf "$tarball" -C "$work" "${members[@]}"

while IFS=$'\t' read -r id text; do
  [[ -z "$id" ]] && continue
  u=${id#*s-}; spk=${u%%-*}; rest=${u#*-}; chap=${rest%%-*}
  ffmpeg -loglevel error -y -i "$work/LibriSpeech/test-clean/$spk/$chap/$u.flac" -ac 1 -ar 16000 -c:a pcm_s16le "$root/clips/$id.wav"
  printf '%s\n' "$text" > "$root/clips/$id.txt"
done < "$here/../clips.tsv"

cp "$here/../CLIPS-LICENSE.txt" "$root/clips/LICENSE.txt"
echo "wrote $(ls "$root"/clips/*.wav | wc -l) clips to $root/clips"
