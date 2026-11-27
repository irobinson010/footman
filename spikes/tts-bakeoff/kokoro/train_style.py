"""Fit an Ashford style vector for the frozen Kokoro-82M by gradient descent (no weight training).

The model stays frozen.  The trainable thing is `delta`, a 256-d offset added to every row of a
stock voice pack (so the length-dependence of the pack is kept); the result is a 510x256 table
that drops into voices.bin as one more speaker.  delta[:128] steers the decoder (timbre),
delta[128:] steers the prosody predictor (rhythm, pitch, energy).

Per line, per epoch:
  1. synthesise with the current style and the model's own durations, DTW-align that audio to the
     real clip, and read off per-phoneme durations that fit the clip;
  2. teacher-force those durations, synthesise with gradients, minimise multi-resolution
     log-mel L1 against the clip (+ a log-L1 on the predictor's own soft durations so the
     predictor half learns the clip's rhythm).

usage: python train_style.py OUT_DIR [--init bm_daniel] [--epochs 6] [--lr 0.01] [--limit N]
"""
import argparse
import json
import random
import time

import torch

from common import ROOT, load_model, load_voice_table, predicted_durations, style_row, synth
from fit_lib import SpkLoss, align_durations, eval_set, load_data, mel_loss

ap = argparse.ArgumentParser()
ap.add_argument("out")
ap.add_argument("--init", default="bm_daniel")
ap.add_argument("--epochs", type=int, default=6)
ap.add_argument("--lr", type=float, default=0.01)
ap.add_argument("--accum", type=int, default=8)
ap.add_argument("--dur-weight", type=float, default=0.5)
ap.add_argument("--spk-weight", type=float, default=0.0, help="weight of the ECAPA speaker-similarity loss")
ap.add_argument("--ecapa", default=str(ROOT / "spk/spkrec-ecapa-voxceleb"))
ap.add_argument("--init-delta", help="delta_eN.pt to continue from")
ap.add_argument("--save-steps", type=int, default=0, help="also save delta every N optimiser steps")
ap.add_argument("--limit", type=int)
ap.add_argument("--val", type=int, default=40)
args = ap.parse_args()

from pathlib import Path
out = Path(args.out)
out.mkdir(parents=True, exist_ok=True)
torch.manual_seed(0)
model = load_model()
train, val = load_data(model, limit=args.limit)
val = val[: args.val]
table = load_voice_table(args.init).cuda()
delta = torch.zeros(256, device="cuda")
if args.init_delta:
    delta = torch.load(args.init_delta)["delta"].cuda()
delta.requires_grad_(True)
spk = SpkLoss(args.ecapa, train) if args.spk_weight else None
opt = torch.optim.Adam([delta], lr=args.lr)
steps_total = args.epochs * (len(train) // args.accum)
sched = torch.optim.lr_scheduler.CosineAnnealingLR(opt, steps_total, eta_min=args.lr * 0.05)
fn = lambda n: style_row(table, n) + delta.detach()
log = []
v0 = eval_set(model, val, fn)
print(f"train {len(train)} val {len(val)}; init {args.init} val mel loss {v0:.4f}", flush=True)
log.append(dict(epoch=0, val=v0))
t0 = time.time()
step = 0
for ep in range(1, args.epochs + 1):
    random.Random(ep).shuffle(train)
    run_m = run_d = run_s = 0.0
    for k, r in enumerate(train, 1):
        ref = (style_row(table, r["ids"].shape[1]) + delta).unsqueeze(0)
        with torch.no_grad():
            dur0 = predicted_durations(model, r["ids"], ref.detach())
            g0, _ = synth(model, r["ids"], ref.detach(), dur0)
            dur = align_durations(g0, dur0, r["wav"]).cuda()
        audio, soft = synth(model, r["ids"], ref, dur)
        lm = mel_loss(audio, r["wav"])
        ld = (torch.log1p(soft) - torch.log1p(dur.float())).abs().mean()
        loss = lm + args.dur_weight * ld
        if spk:
            ls = spk(audio)
            loss = loss + args.spk_weight * ls
            run_s += float(ls)
        (loss / args.accum).backward()
        run_m += float(lm)
        run_d += float(ld)
        if k % args.accum == 0:
            opt.step()
            opt.zero_grad()
            sched.step()
            step += 1
            if args.save_steps and step % args.save_steps == 0:
                torch.save(dict(init=args.init, delta=delta.detach().cpu(), epoch=step), out / f"delta_s{step}.pt")
        if k % 200 == 0:
            print(f"ep {ep} {k}/{len(train)} mel {run_m / 200:.4f} dur {run_d / 200:.4f} spk {run_s / 200:.4f} lr {sched.get_last_lr()[0]:.4f} "
                  f"{time.time() - t0:.0f}s", flush=True)
            run_m = run_d = run_s = 0.0
    v = eval_set(model, val, fn)
    print(f"== epoch {ep} val mel loss {v:.4f}  |delta| dec {delta[:128].norm():.3f} pred {delta[128:].norm():.3f}", flush=True)
    log.append(dict(epoch=ep, val=v, dec=float(delta[:128].norm()), pred=float(delta[128:].norm())))
    torch.save(dict(init=args.init, delta=delta.detach().cpu(), epoch=ep), out / f"delta_e{ep}.pt")
    json.dump(log, open(out / "log.json", "w"), indent=1)

final = (table + delta.detach()).cpu()
torch.save(final, out / "ashford_table.pt")
print("saved", out / "ashford_table.pt", final.shape)
