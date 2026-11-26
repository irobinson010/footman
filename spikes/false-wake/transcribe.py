#!/usr/bin/env python3
"""Transcribes the audio around each false-wake hit, to see what was said.

Usage: uv run --with faster-whisper spikes/false-wake/transcribe.py DATA SETTING [OUT.tsv]
  DATA     the data folder (wav/<type>/*.wav and hits.tsv)
  SETTING  which hits.tsv setting to read, e.g. b2_t0.05

For each wake hit it cuts 5 s before and 1.5 s after the event time with the
ffmpeg CLI (no PyAV decoding), runs faster-whisper small.en on the CPU, and
writes type, file, time, keyword and the transcript.
"""
import csv
import os
import subprocess
import sys
import tempfile

import numpy as np

from faster_whisper import WhisperModel

data, setting = sys.argv[1], sys.argv[2]
out = sys.argv[3] if len(sys.argv) > 3 else os.path.join(data, "transcripts_%s.tsv" % setting)
model = WhisperModel("small.en", device="cpu", compute_type="int8", cpu_threads=8)
rows = [r for r in csv.DictReader(open(os.path.join(data, "hits.tsv")), delimiter="\t")
        if r["setting"] == setting and r["kind"] == "wake"]
with open(out, "w") as f, tempfile.TemporaryDirectory() as tmp:
    f.write("type\tfile\tat_s\tkeyword\ttranscript\n")
    for r in rows:
        at = float(r["at_s"])
        start = max(0.0, at - 5.0)
        wav = os.path.join(tmp, "c.wav")
        subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-ss", "%.2f" % start, "-t", "%.2f" % (at - start + 1.5),
                        "-i", os.path.join(data, "wav", r["type"], r["file"]), "-ac", "1", "-ar", "16000", wav], check=True)
        # decode with the ffmpeg CLI, not PyAV (PyAV breaks on some installs)
        pcm = subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-i", wav, "-f", "f32le", "-ac", "1", "-ar", "16000", "-"],
                             check=True, capture_output=True).stdout
        segs, _ = model.transcribe(np.frombuffer(pcm, dtype=np.float32), language="en", beam_size=1, vad_filter=False)
        text = " ".join(s.text.strip() for s in segs).replace("\t", " ")
        f.write("%s\t%s\t%.1f\t%s\t%s\n" % (r["type"], r["file"], at, r["keyword"], text))
        f.flush()
print("wrote", out)
