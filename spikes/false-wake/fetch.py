#!/usr/bin/env python3
"""Downloads the freely licensed "room audio" set for the false-wake test and
converts it to 16 kHz mono 16-bit WAV under DATA/wav/<type>/.

Usage: fetch.py [DATA]   (default ~/PersonalProjects/footman-data/false-wake)

Types: speech (LibriSpeech dev-other, CC BY 4.0), radio (US 1930s-40s
broadcasts on archive.org, public domain), podcast (archive.org, CC0 / public
domain mark), music (archive.org netlabel, public domain mark), noise
(radio aporee field recordings, public domain mark). Needs ffmpeg. Uses only
the standard library. Writes DATA/sources.tsv. Nothing is committed to git.
"""
import json
import os
import subprocess
import sys
import tarfile
import urllib.parse
import urllib.request

DATA = os.path.expanduser(sys.argv[1] if len(sys.argv) > 1 else "~/PersonalProjects/footman-data/false-wake")
RAW = os.path.join(DATA, "raw")
WAV = os.path.join(DATA, "wav")

# type -> [(archive.org identifier, license note, optional list of file names or None for all mp3)]
ITEMS = {
    "radio": [
        ("708ad762-d325-4df0-98eb-1b6312363a58", "PD (1945 US broadcast)", ["The Fred Allen Show 1945-10-07 Charlie McCarthy Auditions for Fred.mp3"]),
        ("db7b3649-c176-487b-9170-cee8dd85f464", "PD (1939 CBS/WJSV broadcast day)", ["WJSV_Complete_Broadcast_Day_39-09-21_-2245-2321_Repeat_of_FDR_Speech - caa4f383521dd14b.mp3"]),
        ("43b2fbbf-40de-457c-911d-61d649ba7acd", "PD (1945 US broadcast)", None),
        ("0415d393-3dee-49fc-925f-eecbe4ad2df5", "PD (1945 AFRS Jubilee, jazz + talk)", ["AFRS 141 Jubilee Count Basie - Hattie McDaniel 07-16-45 - 22a6dc22c8d0ea59.mp3"]),
        ("066ba7dc-ce47-4b16-a805-2a8a2051d0b2", "PD (1946 US broadcast, songs)", ["Burl Ives Show 46-12-20 First Tune - Get Along Little Doggies - 3c81ab95ebb4fa6e.mp3"]),
        ("891cd024-ec54-4fb0-9e51-1dd552a1ea04", "PD (1930 US broadcast)", None),
    ],
    "podcast": [
        ("at-home-podcast-001-version-4", "CC0", None),
        ("ghostwriter-to-catch-a-creep-part-3-04-09-2025", "CC0", None),
        ("ghostwriter-who-burned-down-mr-brinkers-store-1", "CC0", None),
        ("episode_20--DEdit", "Public Domain Mark", None),
        ("armchairphilosophypodcast-promo1", "CC0", None),
    ],
    "music": [
        ("MARAV958C", "Public Domain Mark (Music and Voice)", None),
        ("MARAV083C", "Public Domain Mark (Hum and Strum)", None),
    ],
    "noise": [
        ("aporee_60198_69141", "Public Domain Mark (Brussels place ambience)", None),
        ("aporee_61023_70148", "Public Domain Mark (Guatemala market)", None),
        ("aporee_69049_86580", "Public Domain Mark (Brussels tram stop)", None),
        ("aporee_59974_68878", "Public Domain Mark (volleyball match crowd)", None),
        ("aporee_60276_69229", "Public Domain Mark (Barcelona urban)", None),
    ],
}
LIBRI = "https://www.openslr.org/resources/12/dev-other.tar.gz"
LIBRI_HOURS = 1.5


def get(url, dest):
    if os.path.exists(dest):
        return
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    print("get", url, flush=True)
    req = urllib.request.Request(url, headers={"User-Agent": "footman-false-wake/1"})
    with urllib.request.urlopen(req, timeout=60) as r, open(dest + ".part", "wb") as f:
        while chunk := r.read(1 << 20):
            f.write(chunk)
    os.rename(dest + ".part", dest)


def to_wav(src, dst):
    if os.path.exists(dst):
        return
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-i", src, "-ac", "1", "-ar", "16000",
                    "-c:a", "pcm_s16le", dst], check=True)


def main():
    sources = []
    for typ, items in ITEMS.items():
        for ident, lic, names in items:
            meta = json.load(urllib.request.urlopen("https://archive.org/metadata/" + ident, timeout=60))
            title = meta["metadata"].get("title", ident)
            files = [f["name"] for f in meta["files"] if f["name"].lower().endswith(".mp3")]
            if names:
                files = [f for f in files if f in names]
            for i, name in enumerate(files):
                raw = os.path.join(RAW, typ, ident, name)
                get("https://archive.org/download/%s/%s" % (urllib.parse.quote(ident), urllib.parse.quote(name)), raw)
                out = os.path.join(WAV, typ, "%s_%02d.wav" % (ident[:40], i))
                to_wav(raw, out)
                sources.append((typ, "https://archive.org/details/" + ident, title, lic, os.path.basename(out)))
    # LibriSpeech dev-other: take speakers' flacs until LIBRI_HOURS.
    tgz = os.path.join(RAW, "speech", "dev-other.tar.gz")
    get(LIBRI, tgz)
    exdir = os.path.join(RAW, "speech", "x")
    if not os.path.isdir(exdir):
        with tarfile.open(tgz) as t:
            t.extractall(exdir, filter="data")
    flacs = sorted(os.path.join(d, f) for d, _, fs in os.walk(exdir) for f in fs if f.endswith(".flac"))
    # one chapter file group per speaker: concatenate utterances of a chapter into a long clip
    chapters = {}
    for p in flacs:
        chapters.setdefault(os.path.dirname(p), []).append(p)
    total = 0.0
    for n, (d, ps) in enumerate(sorted(chapters.items())):
        if total >= LIBRI_HOURS * 3600:
            break
        out = os.path.join(WAV, "speech", "librispeech_dev-other_%03d.wav" % n)
        if not os.path.exists(out):
            lst = os.path.join(d, "list.txt")
            with open(lst, "w") as f:
                for p in sorted(ps):
                    f.write("file '%s'\n" % p.replace("'", "'\\''"))
            os.makedirs(os.path.dirname(out), exist_ok=True)
            subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", lst,
                            "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", out], check=True)
            os.remove(lst)
        total += os.path.getsize(out) / 32000.0
        sources.append(("speech", "https://www.openslr.org/12/", "LibriSpeech dev-other chapter " + os.path.basename(d),
                        "CC BY 4.0", os.path.basename(out)))
    with open(os.path.join(DATA, "sources.tsv"), "w") as f:
        f.write("type\turl\ttitle\tlicense\twav\n")
        for s in sources:
            f.write("\t".join(s) + "\n")


main()
