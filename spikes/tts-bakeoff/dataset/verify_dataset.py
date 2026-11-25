#!/usr/bin/env python3
"""ASR-verify and drift-check a rendered set (CPU only). Companion to render_dataset.py.

Reuses Troupe's calibrated checks (read-only imports from the Troupe checkout):
  - WER via spikes/asr-verify/wer.py (normalizer expands numbers), faster-whisper base.en on CPU
  - chars/second band and trailing-silence level (S0-07 values from troupe.config.QAConfig)
  - speaker drift: ECAPA cosine vs the reference clip, spikes/voice-drift/score.py
Writes OUT/manifest.jsonl (every line, with verdict + reasons) and OUT/stats.json.
Not yet run (written while the GPU was busy); run Troupe's spikes with `uv run` so their deps exist.

    python3 verify_dataset.py --out OUT/indextts --ref ref.wav --troupe ~/PersonalProjects/reading_text_in_real_voice
"""
import argparse
import csv
import json
import math
import sys
import wave
from pathlib import Path

MAX_WER, MIN_CPS, MAX_CPS, MIN_TAIL_DB = 0.08, 6.0, 25.0, -50.0
MIN_SPK, SPK_MARGIN, SPK_MIN_S, MIN_WORD_ERRORS = 0.33, 0.15, 3.0, 2


def tail_db(path):
    import numpy as np
    with wave.open(str(path), "rb") as w:
        n = w.getframerate()
        w.setpos(max(0, w.getnframes() - n))
        x = np.frombuffer(w.readframes(n), dtype=np.int16).astype("float32") / 32768
    return 20 * math.log10(max(float(np.sqrt((x ** 2).mean())), 1e-9))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--ref", required=True, type=Path)
    ap.add_argument("--troupe", required=True, type=Path)
    ap.add_argument("--script", type=Path, default=Path(__file__).parent / "script.tsv")
    a = ap.parse_args()
    sys.path[:0] = [str(a.troupe / "spikes/asr-verify"), str(a.troupe / "spikes/voice-drift")]
    from wer import normalize, word_error_rate  # noqa: E402
    from score import Scorer  # noqa: E402  (class name per voice-drift/score.py; adjust if renamed)
    from faster_whisper import WhisperModel  # noqa: E402

    asr = WhisperModel("base.en", device="cpu", compute_type="int8")
    scorer = Scorer()
    rows = [r for r in csv.DictReader(a.script.open(encoding="utf-8"), delimiter="\t")
            if (a.out / "wavs" / f"{r['id']}.wav").exists()]
    recs = []
    for r in rows:
        p = a.out / "wavs" / f"{r['id']}.wav"
        segs, _ = asr.transcribe(str(p), language="en")
        hyp = " ".join(s.text for s in segs)
        wer = word_error_rate(r["text"], hyp)
        errs = round(wer * max(1, len(normalize(r["text"]))))
        with wave.open(str(p), "rb") as w:
            d = w.getnframes() / w.getframerate()
        recs.append({"id": r["id"], "text": r["text"], "heard": hyp, "wer": wer, "word_errors": errs,
                     "audio_s": d, "cps": len(r["text"]) / d, "tail_db": tail_db(p),
                     "spk": scorer.similarity(a.ref, p)})
    med = sorted(x["spk"] for x in recs if x["audio_s"] >= SPK_MIN_S)
    med = med[len(med) // 2] if med else 0
    kept = dropped = 0
    reasons = {}
    for x in recs:
        why = []
        if x["wer"] > MAX_WER and x["word_errors"] >= MIN_WORD_ERRORS:
            why.append("wer")
        if not MIN_CPS <= x["cps"] <= MAX_CPS:
            why.append("duration")
        if x["tail_db"] < MIN_TAIL_DB:  # dead air at the end (a render that kept going)
            why.append("silent_tail")
        if x["audio_s"] >= SPK_MIN_S and (x["spk"] < MIN_SPK or x["spk"] < med - SPK_MARGIN):
            why.append("drift")
        x["verdict"], x["reasons"] = ("drop" if why else "keep"), why
        kept += not why
        dropped += bool(why)
        for w_ in why:
            reasons[w_] = reasons.get(w_, 0) + 1
    with (a.out / "manifest.jsonl").open("w") as f:
        for x in recs:
            f.write(json.dumps(x) + "\n")
    hours = sum(x["audio_s"] for x in recs if x["verdict"] == "keep") / 3600
    stats = {"lines": len(recs), "kept": kept, "dropped": dropped, "drop_reasons": reasons,
             "kept_hours": round(hours, 3), "median_speaker_similarity": round(med, 3)}
    (a.out / "stats.json").write_text(json.dumps(stats, indent=2))
    print(stats)


if __name__ == "__main__":
    main()
