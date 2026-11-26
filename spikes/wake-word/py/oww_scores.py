#!/usr/bin/env python3
"""openWakeWord helper (spike only; there is no practical Go path for OWW's
melspectrogram -> embedding -> classifier chain without a generic ONNX runtime binding).

Usage:  oww_scores.py OUT.json FILE_OR_LIST... [--vad 0.5] [--dir DATA_DIR]
Each wav (16 kHz mono) is streamed in 1280-sample (80 ms) frames, exactly as live capture would,
and the per-frame score of every ready-made model is written to OUT.json for the Go harness to
threshold (`wakeword oww-hour` / `oww-pos`). CPU time is process CPU time around predict().
Reads raw f32le from stdin when FILE is "-" and prints detections (owner live test, --live).
"""
import argparse, glob, json, os, sys, time
import numpy as np
import soundfile as sf
import onnxruntime as ort
import openwakeword
from openwakeword.model import Model

MODELS = ["alexa", "hey_jarvis", "hey_mycroft"]  # (the package also ships hey_marvin, timer, weather)
FRAME = 1280


def make_model(vad):
    d = os.path.join(os.path.dirname(openwakeword.__file__), "resources", "models")
    paths = [os.path.join(d, f"{m}_v0.1.onnx") for m in MODELS]
    return Model(wakeword_model_paths=paths, vad_threshold=vad)


def live(args):
    m = make_model(args.vad)
    last = {}
    t0 = time.time()
    print("listening; models:", ", ".join(m.models.keys()), file=sys.stderr)
    while True:
        raw = sys.stdin.buffer.read(FRAME * 4)
        if len(raw) < FRAME * 4:
            return
        x = np.frombuffer(raw, np.float32)
        p = m.predict((np.clip(x, -1, 1) * 32767).astype(np.int16))
        now = time.time() - t0
        for k, v in p.items():
            if v >= args.live_thr and now - last.get(k, -9) > 2.0:
                last[k] = now
                print(f"{now:6.1f}s  wake {k} score={v:.2f}", flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out", nargs="?")
    ap.add_argument("files", nargs="*")
    ap.add_argument("--vad", type=float, default=0.0, help="openWakeWord built-in Silero VAD gate (0 = off)")
    ap.add_argument("--dir", default="")
    ap.add_argument("--live", action="store_true")
    ap.add_argument("--live-thr", type=float, default=0.5)
    args = ap.parse_args()
    if args.live:
        return live(args)
    ort.set_default_logger_severity(3)
    files = []
    for f in args.files:
        if f.endswith(".txt"):
            files += [l.strip() for l in open(f) if l.strip()]
        else:
            files.append(f)
    m = make_model(args.vad)
    out = {"frame_s": FRAME / 16000, "files": {}, "cpu_s": 0.0, "audio_s": 0.0}
    for f in files:
        x, sr = sf.read(os.path.join(args.dir, f), dtype="int16")
        assert sr == 16000, f
        m.reset()
        scores = {k.replace("_v0.1", ""): [] for k in m.models.keys()}
        c0 = time.process_time()
        for i in range(0, len(x) - FRAME + 1, FRAME):
            p = m.predict(x[i:i + FRAME])
            for k, v in p.items():
                scores[k.replace("_v0.1", "")].append(round(float(v), 4))
        out["cpu_s"] += time.process_time() - c0
        out["audio_s"] += len(x) / 16000
        out["files"][f] = scores
    json.dump(out, open(args.out, "w"))
    print(f"{len(files)} files, {out['audio_s']:.0f} s audio, cpu {out['cpu_s']:.1f} s "
          f"= {100 * out['cpu_s'] / out['audio_s']:.2f}% of one core", file=sys.stderr)


if __name__ == "__main__":
    main()
