"""Pronunciations (espeak-ng en-us IPA, via piper's espeakbridge) for words that
Footman's lexicon-en-us.txt lacks.

Footman's Piper path looks words up in lexicon-en-us.txt (and a lexicon*.txt in
the model folder first). That file has no "her", "or" or "per" and no rare
names, so Footman would spell them letter by letter. This writes a lexicon in
the same "word p h o n e s" format for the Kokoro-lexicon words it lacks plus
any extra words, to ship beside the Piper model as lexicon.txt.

usage: make_extra_lexicon.py OUT.txt MODELS_DIR PIPER_SRC [extra words...]
"""
import re
import sys
import unicodedata
from pathlib import Path

out_path, models, piper_src, extra = sys.argv[1], Path(sys.argv[2]), sys.argv[3], sys.argv[4:]
sys.path.insert(0, piper_src)
from piper.phonemize_espeak import EspeakPhonemizer  # noqa: E402

have = set()
for line in open(models / "lexicon-en-us.txt", encoding="utf8"):
    if "||" in line and not line.startswith("#"):
        have.add(line.split("||")[0].strip().lower())
words = set(w.lower() for w in extra)
for line in open(models / "kokoro-multi-lang-v1_0/lexicon-us-en.txt", encoding="utf8"):
    if line.strip():
        words.add(line.split()[0].lower())
words = sorted(w for w in words if w not in have and re.fullmatch(r"[a-z][a-z']*", w))
ph = EspeakPhonemizer()
n = 0
with open(out_path, "w", encoding="utf8") as out:
    for w in words:
        s = ph.phonemize("en-us", w)
        p = "".join("".join(x) for x in s).strip(" .")
        p = unicodedata.normalize("NFC", p).replace(" ", "")
        if p:
            out.write(f"{w} {' '.join(p)}\n")
            n += 1
print(n, "words written")
