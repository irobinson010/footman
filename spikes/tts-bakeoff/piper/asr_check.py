# /// script
# requires-python = ">=3.10"
# dependencies = ["faster-whisper"]
# ///
"""Intelligibility check for rendered WAVs: transcribe NN.wav with Whisper
(base.en, CPU) and report the word error rate against NN.txt (the text the
model was asked to say, written by TestRenderSentences).

usage: uv run asr_check.py WAV_DIR
"""
import re
import subprocess
import sys
from pathlib import Path

import numpy as np
from faster_whisper import WhisperModel


def load(path) -> np.ndarray:
    # The PyAV wheel faster-whisper installs refuses metadata_errors, so decode with ffmpeg.
    raw = subprocess.run(["ffmpeg", "-loglevel", "error", "-i", str(path), "-f", "f32le", "-ac", "1", "-ar", "16000", "-"],
                         check=True, capture_output=True).stdout
    return np.frombuffer(raw, dtype=np.float32)


def words(s: str) -> list[str]:
    return re.sub(r"[^a-z' ]", " ", s.lower().replace("-", " ")).split()


def edit_distance(a: list[str], b: list[str]) -> int:
    d = list(range(len(b) + 1))
    for i, x in enumerate(a, 1):
        prev, d[0] = d[0], i
        for j, y in enumerate(b, 1):
            prev, d[j] = d[j], min(d[j] + 1, d[j - 1] + 1, prev + (x != y))
    return d[-1]


model = WhisperModel("base.en", device="cpu", compute_type="int8")
errs = total = 0
for wav in sorted(Path(sys.argv[1]).glob("*.wav")):
    ref = words(wav.with_suffix(".txt").read_text())
    segs, _ = model.transcribe(load(wav), language="en", beam_size=5)
    hyp_text = " ".join(s.text for s in segs)
    e = edit_distance(ref, words(hyp_text))
    errs += e
    total += len(ref)
    print(f"{wav.name}: {e} errors / {len(ref)} words: {hyp_text.strip()}")
print(f"WER {100 * errs / total:.1f}% ({errs}/{total})")
