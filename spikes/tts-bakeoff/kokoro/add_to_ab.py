"""Add a voice to the blind A/B page (footman-data/tts-bakeoff/ab) as one more option per sentence.

usage: python add_to_ab.py AB_DIR VOICE_KEY "Display name" WAV_DIR [--sentences sentences.txt]

Reads the existing index.html (clip files and reveal key per sentence), loudness-normalises
WAV_DIR/NN.wav (ffmpeg loudnorm I=-20 TP=-2, 24 kHz mono) into AB_DIR/clips/<opaque hash>.wav,
shuffles every sentence's options anew (labels A, B, C, ...) and rewrites index.html. The old
clips stay on disk; the old page is kept as index.before-VOICE_KEY.html.
"""
import argparse
import hashlib
import html
import json
import random
import re
import secrets
import subprocess
from pathlib import Path

ap = argparse.ArgumentParser()
ap.add_argument("ab")
ap.add_argument("key")
ap.add_argument("name")
ap.add_argument("wavs")
ap.add_argument("--sentences")
a = ap.parse_args()
ab = Path(a.ab)
page = (ab / "index.html").read_text()

key = json.loads(re.search(r"const K=(\{.*?\}),N=", page).group(1))
names = json.loads(re.search(r",N=(\{.*?\});function", page).group(1))
secs = re.findall(r"<section><h2>(\d+)\. (.*?)</h2>(.*?)</section>", page, re.S)
assert len(secs) == 10, len(secs)
clips = {}  # (sentence number, voice) -> clip file
for num, _, body in secs:
    for lab, src in re.findall(r"<b>([A-Z])</b><audio[^>]* src=\"(clips/[^\"]+)\"", body):
        clips[(int(num), key[f"{num}{lab}"])] = src

(ab / "index.before-" f"{a.key}.html").write_text(page)
names[a.key] = a.name
rng = random.Random(secrets.randbits(64))
new_key, sections = {}, []
for num, text, _ in secs:
    n = int(num)
    src = Path(a.wavs) / f"{n:02d}.wav"
    out = f"clips/{hashlib.sha256(secrets.token_bytes(16)).hexdigest()[:10]}.wav"
    subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-i", str(src), "-af", "loudnorm=I=-20:TP=-2:LRA=11",
                    "-ar", "24000", "-ac", "1", "-c:a", "pcm_s16le", str(ab / out)], check=True)
    clips[(n, a.key)] = out
    voices = [v for (i, v) in clips if i == n]
    rng.shuffle(voices)
    rows = ""
    for j, v in enumerate(voices):
        lab = "ABCDEFGH"[j]
        new_key[f"{n}{lab}"] = v
        rows += (f'<div class=c><b>{lab}</b><audio controls preload=none src="{clips[(n, v)]}"></audio>'
                 f'<label><input type=radio name=q{n} value={lab}> best</label></div>')
    sections.append(f"<section><h2>{n}. {text}</h2>{rows}</section>")

nv = len(names)
head = page[: page.index("<section>")]
head = re.sub(r"<p>.*?</p>", f"<p>{nv} voices per sentence, in shuffled order. Pick the one you like best for each, then press Reveal.</p>", head, count=1)
tail = page[page.index("</section>", page.rindex("<section>")) + len("</section>"):]
tail = re.sub(r"const K=\{.*?\},N=\{.*?\};", lambda m: "const K=" + json.dumps(new_key) + ",N=" + json.dumps(names) + ";", tail, flags=re.S)
tail = re.sub(r"const c=\{[^}]*\}", "const c={" + ",".join(f"{k}:0" for k in names) + "}", tail)
tail = re.sub(r"for\(let i=1;i<=10;i\+\+\)\{.*?\}document", lambda m: m.group(0), tail, flags=re.S)
tail = tail.replace("' | C='+N[K[i+'C']]+", "' | C='+N[K[i+'C']]+' | D='+N[K[i+'D']]+")
(ab / "index.html").write_text(head + "".join(sections) + tail)
print(f"wrote {ab / 'index.html'} with {nv} voices")
