"""Second pass over verify_dataset.py's manifest (S0-09).

1. Trim trailing dead air (last 20 ms frame above -40 dBFS, plus 250 ms) instead of
   dropping the line: the speech itself was fine in every case checked.
2. Re-score WER drops with a date/time-aware normalizer, because faster-whisper writes
   "the twentieth of November" as "20 November" and "7:45" as "745".

Rewrites manifest.jsonl, stats.json and redo.txt in place. Run it from Troupe's
spikes/voice-drift env with num2words:

    uv run --with num2words python postprocess.py DATASET_DIR [ENGINE]
"""
import json
import pathlib
import re
import sys
import wave

import numpy as np
from num2words import num2words

W = pathlib.Path(sys.argv[1])
ENGINE = sys.argv[2] if len(sys.argv) > 2 else "voxcpm2"
sys.path.insert(0, str(pathlib.Path.home() / "PersonalProjects/reading_text_in_real_voice/spikes/asr-verify"))
from wer import normalize, word_error_rate  # noqa: E402

OUT = W / "out" / ENGINE
M = OUT / "manifest.jsonl"
MONTHS = "january|february|march|april|may|june|july|august|september|october|november|december"
ORD = {num2words(i, to="ordinal"): str(i) for i in range(1, 32)}


def canon(t):
    t = t.lower()
    t = re.sub(rf"\bthe (\w+) of ({MONTHS})\b", lambda m: f"{ORD.get(m.group(1), m.group(1))} {m.group(2)}", t)
    t = re.sub(r"\b(\d+)(st|nd|rd|th)\b", r"\1", t)
    return re.sub(r"(\d)[-:](?=\d)", r"\1 ", t)


def trim(p):
    with wave.open(str(p)) as w:
        sr = w.getframerate()
        a = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    hop = int(sr * 0.02)
    f = a.astype(np.float32) / 32768
    loud = [i for i in range(0, len(f) - hop, hop) if 20 * np.log10(np.sqrt((f[i:i + hop] ** 2).mean() + 1e-12)) > -40]
    if not loud:
        return None
    end = min(len(a), loud[-1] + hop + int(sr * 0.25))
    with wave.open(str(p), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(sr)
        w.writeframes(a[:end].tobytes())
    return end / sr


rows = [json.loads(line) for line in M.open()]
trimmed = rescued = 0
for x in rows:
    if "silent_tail" in x["reasons"]:
        d = trim(OUT / "wavs" / f"{x['id']}.wav")
        if d:
            x.update(audio_s=d, cps=len(x["text"]) / d, trimmed=True)
            x["reasons"].remove("silent_tail")
            trimmed += 1
    if "wer" in x["reasons"]:
        w2 = word_error_rate(canon(x["text"]), canon(x["heard"]))
        if not (w2 > 0.08 and round(w2 * max(1, len(normalize(x["text"])))) >= 2):
            x["reasons"].remove("wer")
            x["wer_canon"] = w2
            rescued += 1
    if not 6 <= x["cps"] <= 25 and "duration" not in x["reasons"]:
        x["reasons"].append("duration")
    x["verdict"] = "drop" if x["reasons"] else "keep"
M.write_text("".join(json.dumps(x) + "\n" for x in rows))
kept = [x for x in rows if x["verdict"] == "keep"]
dropped = [x for x in rows if x["verdict"] == "drop"]
reasons = {}
for x in dropped:
    for y in x["reasons"]:
        reasons[y] = reasons.get(y, 0) + 1
s = json.loads((OUT / "stats.json").read_text())
s.update(kept=len(kept), dropped=len(dropped), drop_reasons=reasons,
         kept_hours=round(sum(x["audio_s"] for x in kept) / 3600, 3),
         tails_trimmed=trimmed, wer_rescued_by_date_normalizer=rescued)
(OUT / "stats.json").write_text(json.dumps(s, indent=2))
(OUT / "redo.txt").write_text("".join(x["id"] + "\n" for x in dropped))
print(s)
