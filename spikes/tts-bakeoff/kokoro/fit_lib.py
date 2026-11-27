"""Data, losses and DTW duration alignment for the Kokoro style fit."""
import json
import os
import random
from pathlib import Path

import librosa
import numpy as np
import torch
import torchaudio

from common import HOP, ROOT, SR, load_voice_table, predicted_durations, read_wav, style_row, synth, token_ids

DATASET = Path(os.environ.get("DATASET", Path.home() / "PersonalProjects/footman-data/ashford-dataset/out/voxcpm2"))
# Names Footman's Kokoro lexicons lack are spelled letter by letter at run time; the dataset
# audio says them as words, so those lines are left out of the fit.
SPELLED = {"Lindqvist", "Tomasz", "Kwame", "Priya", "Oyelaran", "Yusuf", "Petrov", "that'll"}

_RES = [(512, 128, 64), (1024, 256, 80), (2048, 512, 96)]
_mels = {}


def _mel(n_fft, hop, n_mels, device):
    key = (n_fft, hop, n_mels, device)
    if key not in _mels:
        _mels[key] = torchaudio.transforms.MelSpectrogram(
            SR, n_fft=n_fft, hop_length=hop, n_mels=n_mels, f_min=0, f_max=12000, power=1.0).to(device)
    return _mels[key]


def logmel(x, n_fft, hop, n_mels):
    return torch.log(_mel(n_fft, hop, n_mels, x.device)(x).clamp(min=1e-4))


def mel_loss(gen, tgt):
    """Multi-resolution log-mel L1 on waveforms trimmed to a common length."""
    n = min(gen.shape[-1], tgt.shape[-1])
    gen, tgt = gen[:n], tgt[:n]
    return sum(torch.nn.functional.l1_loss(logmel(gen, *r), logmel(tgt, *r)) for r in _RES) / len(_RES)


def _cmvn(m):
    return (m - m.mean(1, keepdim=True)) / (m.std(1, keepdim=True) + 1e-3)


def align_durations(gen_audio, dur, tgt_audio):
    """Per-phoneme integer durations (frames of HOP samples) that line the model's phonemes up with
    the real clip: DTW between CMVN log-mels at the duration-frame rate, each target frame is
    assigned to the phoneme at the mean of its matched generated frames."""
    t_frames = max(int(round(tgt_audio.shape[-1] / HOP)), 1)
    g_frames = int(dur.sum())
    gm = _cmvn(logmel(gen_audio, 1200, HOP, 80))[:, :g_frames]
    tm = _cmvn(logmel(tgt_audio, 1200, HOP, 80))[:, :t_frames]
    cost = torch.cdist(gm.T.contiguous(), tm.T.contiguous(), p=1).cpu().numpy() / gm.shape[0]
    _, wp = librosa.sequence.dtw(C=cost, step_sizes_sigma=np.array([[1, 1], [0, 1], [1, 0]]), backtrack=True)
    wp = wp[::-1]
    idx = torch.repeat_interleave(torch.arange(len(dur)), dur.cpu()).numpy()
    sums = np.zeros(wp[:, 1].max() + 1)
    cnt = np.zeros_like(sums)
    np.add.at(sums, wp[:, 1], idx[np.minimum(wp[:, 0], len(idx) - 1)])
    np.add.at(cnt, wp[:, 1], 1)
    tok = np.round(sums / cnt).astype(int)
    out = np.bincount(tok, minlength=len(dur))
    return torch.from_numpy(np.maximum(out, 1)).long()


def load_data(model, device="cuda", limit=None, seed=0):
    rows = []
    for line in open(ROOT / "data" / "phonemes.psv"):
        i, text, ph = line.rstrip("\n").split("|")
        if SPELLED & set(text.replace(",", " ").split()):
            continue
        ids = token_ids(model, ph)
        if ids.shape[1] > 400:
            continue
        rows.append(dict(id=i, text=text, ph=ph, ids=ids.to(device), wav=read_wav(DATASET / "wavs" / f"{i}.wav").to(device)))
    random.Random(seed).shuffle(rows)
    n_val = max(len(rows) // 20, 8)
    val, train = rows[:n_val], rows[n_val:]
    if limit:
        train = train[:limit]
    return train, val


@torch.no_grad()
def eval_set(model, rows, table_fn):
    """Mean mel loss with the model's own (length-matched) durations: what a listener hears."""
    tot = 0.0
    for r in rows:
        ref = table_fn(r["ids"].shape[1]).unsqueeze(0)
        dur = predicted_durations(model, r["ids"], ref)
        g, _ = synth(model, r["ids"], ref, dur)
        d = align_durations(g, dur, r["wav"])
        g, _ = synth(model, r["ids"], ref, d.to(r["ids"].device))
        tot += float(mel_loss(g, r["wav"]))
    return tot / len(rows)


def rank_voices(n=20):
    from common import load_model
    model = load_model()
    train, _ = load_data(model)
    rows = [r for r in train if 20 <= r["ids"].shape[1] <= 120][:n]
    names = sorted(p.stem for p in (ROOT / "base/Kokoro-82M/voices").glob("*.pt") if p.stem[:2] in ("af", "am", "bf", "bm"))
    res = []
    for name in names:
        tab = load_voice_table(name).cuda()
        res.append((eval_set(model, rows, lambda n_: style_row(tab, n_)), name))
        print(f"{res[-1][0]:.4f} {name}", flush=True)
    print("ranked:", [f"{n} {v:.3f}" for v, n in sorted(res)[:8]])


class SpkLoss:
    """1 - cosine between the ECAPA-TDNN embedding of the generated audio and the centroid of
    the kept dataset clips (differentiable; the frozen scorer is the one spk_sim.py reports)."""

    def __init__(self, ecapa_dir, rows, n_centroid=120, device="cuda"):
        from speechbrain.inference.speaker import EncoderClassifier
        self.clf = EncoderClassifier.from_hparams(source=str(ecapa_dir), savedir=str(ecapa_dir), run_opts={"device": device})
        for p in self.clf.mods.parameters():
            p.requires_grad_(False)
        with torch.no_grad():
            es = [self.embed(r["wav"]) for r in rows[:n_centroid]]
        self.centroid = torch.nn.functional.normalize(torch.stack(es).mean(0), dim=0)

    def embed(self, wav24):
        w = torchaudio.functional.resample(wav24, SR, 16000).unsqueeze(0)
        lens = torch.ones(1, device=w.device)
        f = self.clf.mods.compute_features(w)
        f = self.clf.mods.mean_var_norm(f, lens)
        return torch.nn.functional.normalize(self.clf.mods.embedding_model(f, lens).squeeze(), dim=0)

    def __call__(self, gen24):
        return 1.0 - self.embed(gen24) @ self.centroid
