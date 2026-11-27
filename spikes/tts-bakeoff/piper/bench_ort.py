# /// script
# requires-python = ">=3.10"
# dependencies = ["onnxruntime-gpu", "numpy"]
# ///
"""Time the exported Piper ONNX model's forward pass with onnxruntime on the
CPU and, if available, the CUDA provider. Model compute only: the phoneme ids
come from tokens.txt for the same phoneme strings Footman sends (bench input
is a fixed list of IPA strings), with ^ _ $ added as Piper does.

usage: LD_LIBRARY_PATH=$ROOT/.venv/lib/python3.12/site-packages/nvidia/cu13/lib:$ROOT/.venv/lib/python3.12/site-packages/nvidia/cudnn/lib \
         uv run bench_ort.py MODEL_DIR [--gpu]
(onnxruntime-gpu needs CUDA 13 and cuDNN 9; the training venv's torch wheels carry them.)
"""
import resource
import subprocess
import sys
import time
from pathlib import Path

import numpy as np
import onnxruntime as ort

d = Path(sys.argv[1])
use_gpu = "--gpu" in sys.argv
tok = {}
for line in (d / "tokens.txt").read_text(encoding="utf8").splitlines():
    sym, _, i = line.rpartition(" ")
    tok[sym or " "] = int(i)

# Same phonemes Footman produces for "Very good, sir. The kettle is on." and a
# 31-word sentence's worth of phonemes.
short = "vˈɛɹi ɡˈʊd, sˌɜː. ðə kˈɛɾ ə l ɪz ˈɑːn."
long_ = ("ɪ hˈæv tˈaɪdid ðə stˈʌdi, fˈaɪld ðə θɹˈiː ɹɪpˈɔːɹts juː lˈɛft ˈɑːn ðə dˈɛsk, "
         "ænd bˈʊkt ðə sˈɛvən fˈɔːɹɾi fˈaɪv tɹˈeɪn tə lˈʌndən fɔːɹ θˈɜːzdeɪ.")


def ids(ph: str) -> np.ndarray:
    seq = [tok["^"], tok["_"]]
    for c in ph:
        if c in tok:
            seq += [tok[c], tok["_"]]
    seq.append(tok["$"])
    return np.array([seq], dtype=np.int64)


def run(provider: str):
    so = ort.SessionOptions()
    so.intra_op_num_threads = 8
    sess = ort.InferenceSession(str(next(d.glob("*.onnx"))), so, providers=[provider])
    out = {}
    for name, ph in (("short", short), ("long", long_)):
        x = ids(ph)
        feed = {"input": x, "input_lengths": np.array([x.shape[1]], dtype=np.int64),
                "scales": np.array([0.667, 1.0, 0.8], dtype=np.float32)}
        sess.run(None, feed)  # warm up
        times, secs = [], 0.0
        for _ in range(10):
            t = time.perf_counter()
            audio = sess.run(None, feed)[0]
            times.append(time.perf_counter() - t)
            secs = audio.size / 22050
        times.sort()
        med = times[len(times) // 2]
        out[name] = (med * 1000, med / secs, secs)
    return out


providers = ["CPUExecutionProvider"] + (["CUDAExecutionProvider"] if use_gpu else [])
for p in providers:
    if p not in ort.get_available_providers():
        print(p, "not available")
        continue
    r = run(p)
    rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss // 1024
    vram = ""
    if p.startswith("CUDA"):
        vram = subprocess.run(["nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader"],
                              capture_output=True, text=True).stdout.strip().replace("\n", "; ")
    print(p, {k: f"{ms:.0f} ms for {s:.1f} s audio, RTF {rtf:.3f}" for k, (ms, rtf, s) in r.items()}, f"peak RSS {rss} MB", vram)
