#!/usr/bin/env bash
# Fine-tune a Piper (VITS) base checkpoint on the Ashford dataset (S0-10).
#
#   train.sh RUN BASE_CKPT MAX_EPOCHS [extra piper.train args...]
#
# RUN is a name for the run folder under $ROOT/runs (for example "nem" or
# "john"). Pass --model.use_mrd true for a base that was trained with the
# multi-resolution discriminator (northern_english_male). Re-running with the
# same RUN resumes from the newest runs/RUN/lightning_logs/version_*/checkpoints/last.ckpt (Lightning's --ckpt_path) instead of
# warm-starting, so a killed run loses at most one epoch.
#
# ROOT holds the venv, the piper1-gpl source, and data/ (see prepare.sh).
set -euo pipefail
ROOT=${ROOT:-/home/ian/PersonalProjects/footman-data/ashford-piper-train}
RUN=$1 BASE=$2 EPOCHS=$3
shift 3
OUT=$ROOT/runs/$RUN
mkdir -p "$OUT/cache"
cd "$ROOT/piper1-gpl/src"
export PYTHONPATH=$ROOT/piper1-gpl/src
export PYTHONUNBUFFERED=1
export PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True

RESUME=()
WARM=(--model.warmstart_ckpt "$BASE")
LAST=$(ls -t "$OUT"/lightning_logs/version_*/checkpoints/last.ckpt 2>/dev/null | head -1 || true)
if [ -n "$LAST" ]; then
  RESUME=(--ckpt_path "$LAST")
  WARM=()
fi

exec "$ROOT/.venv/bin/python" -m piper.train fit \
  --data.voice_name ashford \
  --data.csv_path "$ROOT/data/metadata.csv" \
  --data.audio_dir "$ROOT/data/wav22" \
  --data.cache_dir "$OUT/cache" \
  --data.config_path "$OUT/config.json" \
  --data.phoneme_type text \
  --data.espeak_voice en-us \
  --data.batch_size "${BATCH:-16}" \
  --data.validation_split 0.03 \
  --data.num_test_examples 8 \
  --data.num_workers 6 \
  --model.sample_rate 22050 \
  --trainer.max_epochs "$EPOCHS" \
  --trainer.default_root_dir "$OUT" \
  --data.length_bucketing true \
  --trainer.check_val_every_n_epoch "${VAL_EVERY:-5}" \
  "${WARM[@]}" "${RESUME[@]}" "$@"
