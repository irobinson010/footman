#!/usr/bin/env bash
# One-time environment for the Piper fine-tune (S0-10). No sudo, no system packages.
#
#   setup.sh
#
# Creates $ROOT/.venv (Python 3.12 via uv), installs the training dependencies
# plus the piper-tts wheel (for espeak-ng's bridge and data, which the training
# source needs to import), gets the piper1-gpl training source and builds its
# monotonic-align extension, and downloads the base checkpoints.
set -euo pipefail
ROOT=${ROOT:-$HOME/PersonalProjects/footman-data/ashford-piper-train}
mkdir -p "$ROOT/base/northern_english_male" "$ROOT/base/john"
cd "$ROOT"
uv venv --python 3.12 .venv
uv pip install --python .venv/bin/python "torch>=2.5,<3" torchaudio "lightning>=2,<3" tensorboard tensorboardX \
  "jsonargparse[signatures]>=4.27.7" onnx onnxscript "pysilero-vad>=2.1,<3" "cython>=3,<4" "librosa<1" \
  onnxruntime pathvalidate piper-tts num2words soundfile

# The training code is the OHF-Voice/piper1-gpl source (GPL-3.0), commit 6aacdbc
# (v1.8.0). It is not vendored in Footman.
git clone --depth 1 https://github.com/OHF-Voice/piper1-gpl.git piper1-gpl
SP=$ROOT/.venv/lib/python3.12/site-packages/piper
cp "$SP/espeakbridge.so" piper1-gpl/src/piper/
cp -r "$SP/espeak-ng-data" piper1-gpl/src/piper/
(cd piper1-gpl/src/piper/train/vits/monotonic_align && mkdir -p monotonic_align &&
  "$ROOT/.venv/bin/cythonize" -i core.pyx && mv core*.so monotonic_align/)

# Base checkpoints (rhasspy/piper-checkpoints on Hugging Face).
B=https://huggingface.co/datasets/rhasspy/piper-checkpoints/resolve/main/en
curl -L -o base/northern_english_male/last.ckpt "$B/en_GB/northern_english_male/medium/epoch=2999-step=384000.ckpt"
curl -L -o base/john/john-2599.ckpt "$B/en_US/john/medium/john-2599.ckpt"
