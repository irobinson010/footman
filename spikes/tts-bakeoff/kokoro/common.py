"""Shared pieces for the Kokoro style-vector fit (S0-10, Kokoro half).

Loads the frozen Kokoro-82M v1.0 weights with the `kokoro` PyPI package's
modules, and re-implements its forward pass so that (a) gradients flow to the
256-d style vector and (b) per-phoneme durations can be supplied instead of
predicted (teacher forcing).  Style layout: ref_s[:, :128] drives the decoder
(timbre), ref_s[:, 128:] drives the prosody predictor (duration, F0, energy).
"""
import json
import os
from pathlib import Path

import soundfile as sf
import torch
import torch.nn as nn

ROOT = Path(os.environ.get("KROOT", Path.home() / "PersonalProjects/footman-data/kokoro-train"))
BASE = ROOT / "base" / "Kokoro-82M"
SR = 24000
HOP = 600  # audio samples per aligned (duration) frame


def load_model(device="cuda"):
    from kokoro.model import KModel
    cfg = json.load(open(BASE / "config.json"))
    m = KModel(repo_id="hexgrad/Kokoro-82M", config=cfg, model=str(BASE / "kokoro-v1_0.pth")).to(device).eval()
    for p in m.parameters():
        p.requires_grad_(False)
    # cuDNN refuses LSTM backward in eval mode; these LSTMs are single-layer so train() only
    # changes that (their dropout argument is inert).
    for mod in m.modules():
        if isinstance(mod, nn.LSTM):
            mod.train()
    return m


def load_voice_table(name):
    """Kokoro voice pack: (510, 1, 256) -> (510, 256)."""
    return torch.load(BASE / "voices" / f"{name}.pt", weights_only=True).reshape(510, 256)


def token_ids(model, phonemes):
    ids = [model.vocab[p] for p in phonemes if p in model.vocab]
    return torch.LongTensor([[0, *ids, 0]])


def style_row(table, n_phonemes):
    """Row used for an utterance with n_phonemes symbols (kokoro: pack[len(ps)-1])."""
    return table[min(max(n_phonemes - 1, 0), 509)]


def read_wav(path):
    x, sr = sf.read(path, dtype="float32")
    x = torch.from_numpy(x if x.ndim == 1 else x.mean(1))
    if sr != SR:  # the dataset wavs are 48 kHz
        import torchaudio
        x = torchaudio.functional.resample(x, sr, SR)
    return x


def text_side(model, input_ids, s_pred):
    """Durations side: returns text features d (1,N,512), soft durations (N,), lengths, mask."""
    n = input_ids.shape[-1]
    lengths = torch.full((1,), n, device=input_ids.device, dtype=torch.long)
    mask = torch.zeros(1, n, dtype=torch.bool, device=input_ids.device)
    bert = model.bert(input_ids, attention_mask=(~mask).int())
    d_en = model.bert_encoder(bert).transpose(-1, -2)
    d = model.predictor.text_encoder(d_en, s_pred, lengths.cpu(), mask)
    x, _ = model.predictor.lstm(d)
    soft = torch.sigmoid(model.predictor.duration_proj(x)).sum(-1).squeeze(0)
    return d, soft, lengths, mask


def synth(model, input_ids, ref_s, dur):
    """Differentiable synthesis with given integer durations dur (N,). Returns (audio, soft durations)."""
    s_dec, s_pred = ref_s[:, :128], ref_s[:, 128:]
    d, soft, lengths, mask = text_side(model, input_ids, s_pred)
    n = input_ids.shape[1]
    idx = torch.repeat_interleave(torch.arange(n, device=input_ids.device), dur)
    aln = torch.zeros(n, idx.shape[0], device=input_ids.device)
    aln[idx, torch.arange(idx.shape[0], device=input_ids.device)] = 1
    en = d.transpose(-1, -2) @ aln.unsqueeze(0)
    f0, nz = model.predictor.F0Ntrain(en, s_pred)
    t_en = model.text_encoder(input_ids, lengths, mask)
    asr = t_en @ aln.unsqueeze(0)
    audio = model.decoder(asr, f0, nz, s_dec).squeeze()
    return audio, soft


def predicted_durations(model, input_ids, ref_s, speed=1.0):
    with torch.no_grad():
        _, soft, _, _ = text_side(model, input_ids, ref_s[:, 128:])
        return torch.round(soft / speed).clamp(min=1).long()
