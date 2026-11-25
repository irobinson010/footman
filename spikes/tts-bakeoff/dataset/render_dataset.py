#!/usr/bin/env python3
"""Render script.tsv through a running Troupe TTS engine container (GPU half of S0-09).

NOT RUN YET: written while the GPU was busy, so the HTTP shape is copied from Troupe's adapters
(src/troupe/engines/indextts.py and voxcpm2.py: POST {url}/generate, multipart fields `text`,
`reference` (wav), optional `seed`) and is untested against a live container. Smoke-test with --limit 5.

    python3 render_dataset.py --url http://127.0.0.1:8095 --ref ref.wav --out OUT/indextts  [--limit N]

Resumable: lines whose wav already exists are skipped. Writes OUT/wavs/<id>.wav and
OUT/render.jsonl (id, audio_s, wall_s, rtf, seed, attempt). Needs `pip install httpx`.
"""
import argparse
import csv
import json
import time
import wave
from pathlib import Path

import httpx


def dur(p):
    with wave.open(str(p), "rb") as w:
        return w.getnframes() / w.getframerate()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--ref", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--script", type=Path, default=Path(__file__).parent / "script.tsv")
    ap.add_argument("--limit", type=int)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--only", help="file of ids to (re)render, one per line")
    a = ap.parse_args()
    wavs = a.out / "wavs"
    wavs.mkdir(parents=True, exist_ok=True)
    rows = list(csv.DictReader(a.script.open(encoding="utf-8"), delimiter="\t"))
    if a.only:
        ids = set(Path(a.only).read_text().split())
        rows = [r for r in rows if r["id"] in ids]
    if a.limit:
        rows = rows[: a.limit]
    ref = a.ref.read_bytes()
    log = (a.out / "render.jsonl").open("a")
    with httpx.Client(timeout=300) as c:
        for i, r in enumerate(rows, 1):
            p = wavs / f"{r['id']}.wav"
            if p.exists():
                continue
            t0 = time.monotonic()
            resp = c.post(f"{a.url}/generate",
                          files={"text": (None, r["text"]), "reference": ("ref.wav", ref, "audio/wav"),
                                 "seed": (None, str(a.seed))})
            if resp.status_code == 503:  # busy
                time.sleep(2)
                resp = c.post(f"{a.url}/generate", files={"text": (None, r["text"]),
                              "reference": ("ref.wav", ref, "audio/wav"), "seed": (None, str(a.seed))})
            resp.raise_for_status()
            p.write_bytes(resp.content)
            wall, audio = time.monotonic() - t0, dur(p)
            log.write(json.dumps({"id": r["id"], "audio_s": round(audio, 3), "wall_s": round(wall, 3),
                                  "rtf": round(wall / audio, 3), "seed": a.seed}) + "\n")
            log.flush()
            if i % 50 == 0:
                print(f"{i}/{len(rows)}", flush=True)


if __name__ == "__main__":
    main()
