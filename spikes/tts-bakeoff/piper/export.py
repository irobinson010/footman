"""Export a trained Piper checkpoint as a model folder Footman's sherpa-onnx
Piper loader accepts (ADR-0037, "How the trained model replaces it").

The folder gets:
  ashford.onnx        piper.train.export_onnx output plus the sherpa-onnx
                      metadata (model_type=vits, comment=piper, ...) that the
                      stock vits-piper-* models carry
  ashford.onnx.json   the training config, with the inference defaults
  tokens.txt          "symbol id" per line, from the phoneme_id_map
  lexicon.txt         Footman looks here first (words its en-us lexicon lacks)
  MODEL_CARD

usage: export.py CKPT OUT_DIR [--lexicon FILE] [--config FILE] [--piper-src DIR]
"""
import argparse
import json
import shutil
import subprocess
import sys
from pathlib import Path

import onnx

ap = argparse.ArgumentParser()
ap.add_argument("ckpt")
ap.add_argument("out_dir")
ap.add_argument("--config", required=True, help="config.json written by training")
ap.add_argument("--lexicon", help="lexicon.txt to ship beside the model")
ap.add_argument("--piper-src", required=True, help="piper1-gpl/src")
ap.add_argument("--name", default="ashford")
ap.add_argument("--card", help="MODEL_CARD text file")
a = ap.parse_args()

out = Path(a.out_dir)
out.mkdir(parents=True, exist_ok=True)
model = out / f"{a.name}.onnx"
subprocess.run(
    [sys.executable, "-m", "piper.train.export_onnx", "--checkpoint", a.ckpt, "--output-file", str(model)],
    check=True,
    env={"PYTHONPATH": a.piper_src, "PATH": "/usr/bin:/bin"},
)

cfg = json.loads(Path(a.config).read_text())
# Sherpa-onnx's metadata for Piper models, as on vits-piper-en_US-norman-medium.
m = onnx.load(str(model))
del m.metadata_props[:]
for k, v in {
    "model_type": "vits",
    "comment": "piper",
    "language": "English",
    "voice": "en",
    "has_espeak": "1",
    "n_speakers": str(cfg.get("num_speakers", 1)),
    "sample_rate": str(cfg["audio"]["sample_rate"]),
}.items():
    p = m.metadata_props.add()
    p.key, p.value = k, v
onnx.save(m, str(model))

# tokens.txt: "symbol id" per line, the single-codepoint symbols with id <= 156,
# the same set as the stock en_US voices (Footman and
# sherpa-onnx send codepoints; the digraphs in piper's map, such as "aɪ", are
# never used). Footman only sends codepoints this file lists.
ids = cfg["phoneme_id_map"]
lines = [f"{sym} {i[0]}" for sym, i in ids.items() if len(sym) == 1 and i[0] <= 156]
(out / "tokens.txt").write_text("\n".join(sorted(lines)) + "\n", encoding="utf8")

cfg["phoneme_type"] = "espeak"  # what Footman's G2P emits (espeak-style IPA)
cfg["espeak"] = {"voice": "en-us"}
(out / f"{a.name}.onnx.json").write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf8")
if a.lexicon:
    shutil.copy(a.lexicon, out / "lexicon.txt")
if a.card:
    shutil.copy(a.card, out / "MODEL_CARD")
print("exported", model, model.stat().st_size // 1024, "KiB")
