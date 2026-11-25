#!/usr/bin/env bash
# Cross-compile stt-bench.exe on Linux and zip it for the ZenBook.
#
#   scripts/fetch-clips.sh && scripts/package-windows.sh
#   -> dist/footman-stt-zenbook.zip
#
# The C compiler is `zig cc` (uv tool install ziglang gives a `python-zig` command;
# set ZIG="zig" if you have a native zig). Only the sherpa-onnx engine is linked
# (its Go module ships prebuilt Windows DLLs, built with MinGW, which zig links
# against); whisper.cpp on Windows runs through the pure-Go whispercli engine
# driving the official prebuilt whisper-cli.exe, which the run script downloads.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
zig=${ZIG:-python-zig}
name=footman-stt-zenbook
out=$root/dist/$name
rm -rf "$out"; mkdir -p "$out"

[[ -d "$root/clips" ]] || { echo "run scripts/fetch-clips.sh first" >&2; exit 1; }

cd "$root"
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  CC="$zig cc -target x86_64-windows-gnu" CXX="$zig c++ -target x86_64-windows-gnu" \
  go build -p "${JOBS:-4}" -tags sherpa -trimpath -o "$out/stt-bench.exe" .

libs=$(go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-windows/v2)/lib/x86_64-pc-windows-gnu
cp "$libs/onnxruntime.dll" "$libs/sherpa-onnx-c-api.dll" "$out/"
chmod u+w "$out"/*.dll

cp -r clips "$out/clips"
cp "$here/models.lock" "$out/models.lock"
cp "$here/zenbook-run.ps1" "$out/run.ps1"
cp "$root/ZENBOOK.md" "$out/README.md"
cp "$root/CLIPS-LICENSE.txt" "$out/"
printf '@echo off\r\nrem Unblock the files Windows marked as downloaded, then run the benchmark.\r\npowershell -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem -Recurse | Unblock-File"\r\npowershell -NoProfile -ExecutionPolicy Bypass -File "%%~dp0run.ps1" %%*\r\npause\r\n' > "$out/run.cmd"

python3 -I - "$root/dist" "$name" <<'EOF'
import os, sys, zipfile
dist, name = sys.argv[1:]
zp = os.path.join(dist, name + ".zip")
with zipfile.ZipFile(zp, "w", zipfile.ZIP_DEFLATED) as z:
    for d, _, fs in os.walk(os.path.join(dist, name)):
        for f in fs:
            p = os.path.join(d, f)
            z.write(p, os.path.relpath(p, dist))
print(zp, os.path.getsize(zp) // 1024, "KiB")
EOF
