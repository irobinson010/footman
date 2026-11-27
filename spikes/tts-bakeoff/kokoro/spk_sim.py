"""Speaker similarity of rendered voices to the Ashford reference: ECAPA-TDNN
(speechbrain/spkrec-ecapa-voxceleb, the scorer the S0-09 dataset check used), cosine of the
192-d embeddings.  Reports, per WAV directory, the mean cosine to (a) the reference clip and
(b) the centroid of 60 kept dataset clips, and for the dataset clips themselves as the ceiling.

usage: python spk_sim.py REF_WAV DATASET_DIR ECAPA_DIR WAV_DIR [WAV_DIR ...]
  REF_WAV     ashford-dataset/ref/ashford.wav
  DATASET_DIR ashford-dataset/out/voxcpm2  (wavs/ + manifest.jsonl)
  ECAPA_DIR   a local copy of speechbrain/spkrec-ecapa-voxceleb
"""
import json
import sys
from pathlib import Path

import soundfile as sf
import torch
import torchaudio.functional as F
from speechbrain.inference.speaker import EncoderClassifier

ref, ds, ecapa, *dirs = sys.argv[1:]
clf = EncoderClassifier.from_hparams(source=ecapa, savedir=ecapa, run_opts={"device": "cpu"})


def emb(path):
    x, sr = sf.read(str(path), dtype="float32", always_2d=True)
    w = torch.from_numpy(x.mean(1))
    if sr != 16000:
        w = F.resample(w, sr, 16000)
    with torch.no_grad():
        e = clf.encode_batch(w[: 30 * 16000].unsqueeze(0)).squeeze()
    return torch.nn.functional.normalize(e, dim=0)


# Second, independent scorer (the fit minimises ECAPA, so ECAPA alone flatters it):
# microsoft/wavlm-base-plus-sv x-vectors.
from transformers import AutoFeatureExtractor, WavLMForXVector
_fx = AutoFeatureExtractor.from_pretrained("microsoft/wavlm-base-plus-sv")
_wl = WavLMForXVector.from_pretrained("microsoft/wavlm-base-plus-sv").eval()


def emb_wavlm(path):
    x, sr = sf.read(str(path), dtype="float32", always_2d=True)
    w = F.resample(torch.from_numpy(x.mean(1)), sr, 16000)[: 20 * 16000]
    inp = _fx(w.numpy(), sampling_rate=16000, return_tensors="pt")
    with torch.no_grad():
        return torch.nn.functional.normalize(_wl(**inp).embeddings.squeeze(), dim=0)


kept = [json.loads(line)["id"] for line in open(Path(ds) / "manifest.jsonl") if '"keep"' in line]
clips = [emb(Path(ds) / "wavs" / f"{i}.wav") for i in kept[::len(kept) // 60][:60]]
centroid = torch.nn.functional.normalize(torch.stack(clips).mean(0), dim=0)
e_ref = emb(ref)
print(f"dataset clips (60): to ref {sum(float(c @ e_ref) for c in clips) / len(clips):.3f}, to centroid {sum(float(c @ centroid) for c in clips) / len(clips):.3f}")
w_clips = [emb_wavlm(Path(ds) / "wavs" / f"{i}.wav") for i in kept[::len(kept) // 60][:60]]
w_cent = torch.nn.functional.normalize(torch.stack(w_clips).mean(0), dim=0)
print(f"dataset clips (60): WavLM to centroid {sum(float(c @ w_cent) for c in w_clips) / len(w_clips):.3f}")
for d in dirs:
    ps = sorted(Path(d).glob("*.wav"))
    es = [emb(p) for p in ps]
    ws = [emb_wavlm(p) for p in ps]
    print(f"{d}: ECAPA to ref {sum(float(e @ e_ref) for e in es) / len(es):.3f}, to centroid {sum(float(e @ centroid) for e in es) / len(es):.3f}; "
          f"WavLM to centroid {sum(float(e @ w_cent) for e in ws) / len(ws):.3f}")
