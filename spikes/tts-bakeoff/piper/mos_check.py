"""UTMOS (the predictor piper.train logs as val_mos) for rendered WAVs, plus the
same score for reference audio (kept dataset lines) to compare against.

usage: PYTHONPATH=$ROOT/piper1-gpl/src $ROOT/.venv/bin/python mos_check.py WAV_DIR [REF_WAV_DIR]
Roughly 1-5, higher is better; it measures naturalness, not accent or accuracy.
"""
import sys
from pathlib import Path

import librosa
import torch
from piper.train.vits.mos import MosPredictor

predictor = MosPredictor()


def score_dir(d: str, limit: int = 40) -> float:
    scores = []
    for wav in sorted(Path(d).glob("*.wav"))[:limit]:
        audio, sr = librosa.load(wav, sr=None, mono=True)
        scores.append(float(predictor.score(torch.from_numpy(audio), sr)))
    return sum(scores) / len(scores)


print(f"{sys.argv[1]}: UTMOS {score_dir(sys.argv[1]):.2f}")
if len(sys.argv) > 2:
    print(f"{sys.argv[2]}: UTMOS {score_dir(sys.argv[2]):.2f}")
