"""Append the fitted Ashford style table to a copy of Footman's Kokoro model folder.

Writes OUT/ as a complete model folder the sherpa-onnx loader (and Footman) accepts:
  voices.bin   the stock table (54 voices, float32 510x256 each) plus the new speaker as id 54
  model.onnx   the stock graph with the speaker metadata (n_speakers, speaker_names, id2speaker,
               speaker2id) extended so Footman can select the new speaker by name
  everything else symlinked to the stock folder (nothing in the stock folder is modified)

usage: python export_voice.py TABLE.pt OUT_DIR [--name bm_ashford] [--src ~/.local/share/footman/models/kokoro-multi-lang-v1_0]
The "b" prefix makes Footman pick British pronunciation (lexicon-gb-en.txt).
"""
import argparse
import os
import shutil
from pathlib import Path

import numpy as np
import onnx
import torch

ap = argparse.ArgumentParser()
ap.add_argument("table")
ap.add_argument("out")
ap.add_argument("--name", default="bm_ashford")
ap.add_argument("--src", default=str(Path.home() / ".local/share/footman/models/kokoro-multi-lang-v1_0"))
a = ap.parse_args()
src, out = Path(a.src), Path(a.out)
out.mkdir(parents=True, exist_ok=True)

for p in src.iterdir():
    if p.name in ("voices.bin", "model.onnx"):
        continue
    dst = out / p.name
    if dst.is_symlink() or dst.exists():
        dst.unlink()
    dst.symlink_to(p.resolve())

stock = np.fromfile(src / "voices.bin", dtype="<f4").reshape(-1, 510, 256)
tab = torch.load(a.table, weights_only=True).reshape(1, 510, 256).numpy().astype("<f4")
assert tab.shape == (1, 510, 256) and np.isfinite(tab).all()
np.concatenate([stock, tab]).tofile(out / "voices.bin")
n = stock.shape[0]

m = onnx.load(str(src / "model.onnx"))
meta = {p.key: p for p in m.metadata_props}
names = meta["speaker_names"].value.split(",")
assert len(names) == n, (len(names), n)
names.append(a.name)
meta["speaker_names"].value = ",".join(names)
meta["n_speakers"].value = str(len(names))
meta["id2speaker"].value = ",".join(f"{i}->{s}" for i, s in enumerate(names))
meta["speaker2id"].value = ",".join(f"{s}->{i}" for i, s in enumerate(names))
onnx.save(m, str(out / "model.onnx"))
print(f"wrote {out}: {len(names)} speakers, {a.name} = id {n}")
