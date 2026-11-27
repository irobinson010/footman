# STT latency run on the laptop (Windows, Intel Core Ultra)

About 5 minutes of your time. The rest is the laptop working. Plug it in and leave it alone while it runs.

1. **Download** `footman-stt-laptop.zip` (12 MB) from the link you were sent.
2. **Unzip** it anywhere (right click, Extract All). A folder named `footman-stt-laptop` appears.
3. **Run one thing:** open that folder and double-click **`run.cmd`**.
   Or in PowerShell: `cd footman-stt-laptop; .\run.cmd`
   - If Windows SmartScreen says "Windows protected your PC", click **More info**, then **Run anyway**. The exe is unsigned.
   - It downloads about 1 GB of models (checksum-checked) on the first run, then benchmarks for roughly 15 minutes. A progress line prints for each row.
   - For the bigger models too (turbo and Parakeet, about +1.7 GB and +20 minutes): `run.cmd -Full`
4. **Send back** `laptop-results.zip`, which appears in the same folder when it finishes. It holds three small files: the timings (`laptop.jsonl`), a readable table (`laptop.md`) and what hardware was detected (`detect.json`).

If a row prints "skipped", leave it; the rest still runs and the skip itself is a result. If the window closes by itself or nothing happens, take a photo or screenshot of it and send that instead.

## What it measures

| Row | Engine | Runs on |
|---|---|---|
| sherpa-onnx, Whisper base.en and small.en | cgo build in `stt-bench.exe` | CPU, and the Arc iGPU through DirectML |
| whisper.cpp, Whisper base.en and small.en | prebuilt `whisper-cli.exe` from the official release, driven by `stt-bench.exe` | CPU |

Each row times 6 LibriSpeech clips (two each of about 5, 10 and 30 seconds) three times and reports the median time from end of audio to text, real-time factor, memory and word error rate.

What is not covered, on purpose, so the numbers are not mistaken for the whole picture:

- **whisper.cpp on the Arc iGPU (Vulkan).** The official Windows release has no Vulkan build, and building one needs Visual Studio Build Tools plus the Vulkan SDK, which is more than a copy-paste job. It is a follow-up (see below).
- **The NPU / OpenVINO.** Not attempted; the ADR-0010 plan puts OpenVINO later.
- Peak memory for the whisper.cpp rows (the exe runs it as a separate process); the sherpa rows have it.

## Notes for whoever maintains this

- Built on Linux with `scripts/package-windows.sh` (Go plus `zig cc` as the cross C compiler; the sherpa-onnx Go module ships its Windows DLLs). It has not been run on Windows by the author; if `stt-bench.exe` fails to start, that is the first thing to report.
- `run.ps1` is plain PowerShell and readable. It writes only inside the unzipped folder and `%LOCALAPPDATA%\footman-spike`. Delete both to clean up.
- To try the Vulkan path later, someone with VS Build Tools and the Vulkan SDK can build `whisper-cli.exe` from whisper.cpp tag v1.9.6 with `cmake -DGGML_VULKAN=ON`, then run
  `stt-bench.exe -engine whispercli -whisper-cli <path> -model whisper-small.en -accel vulkan -label laptop -out results\laptop.jsonl`.
  Note `-accel vulkan` is only a label for the external binary; `-accel cpu` makes it pass `-ng` (no GPU).
