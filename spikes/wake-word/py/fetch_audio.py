#!/usr/bin/env python3
"""Assemble ~1 hour of realistic background audio from freely licensed corpora.

Sources (all freely redistributable / research licences, nothing is committed):
  * LibriSpeech dev-clean (CC BY 4.0)       read speech, podcast/audiobook-like
  * AMI meeting corpus, IHM (CC BY 4.0)     spontaneous multi-speaker conversation
  * Free Music Archive fma_small (CC tracks only; licence read from tracks.csv)

Output: data/bg/hour.wav (16 kHz mono int16) and data/bg/manifest.json.
Mix plan (minutes): 20 audiobook speech, 12 conversation, 15 music,
13 "TV" (speech mixed over music at 8-14 dB SNR).
"""
import io, json, os, random, subprocess, sys, tarfile, urllib.request
import numpy as np, soundfile as sf
from scipy.signal import resample_poly

SR = 16000
ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "data", "bg")
os.makedirs(ROOT, exist_ok=True)
rng = random.Random(1234)
manifest = {"libri": [], "ami": [], "fma": []}


def to16k(x, sr):
    if x.ndim > 1:
        x = x.mean(axis=1)
    if sr != SR:
        g = np.gcd(sr, SR)
        x = resample_poly(x, SR // g, sr // g)
    return x.astype(np.float32)


def norm(x, dbfs=-24.0):
    r = np.sqrt(np.mean(x ** 2) + 1e-12)
    return np.clip(x * (10 ** (dbfs / 20) / r), -1, 1)


# ---------- LibriSpeech ----------
def libri(minutes):
    cache = os.path.join(ROOT, "libri.npy")
    if os.path.exists(cache):
        return np.load(cache)
    url = "https://www.openslr.org/resources/12/dev-clean.tar.gz"
    print("LibriSpeech: streaming", url, flush=True)
    out, total, n = [], 0, 0
    with urllib.request.urlopen(url) as r, tarfile.open(fileobj=r, mode="r|gz") as tf:
        for m in tf:
            if not m.name.endswith(".flac"):
                continue
            n += 1
            if n % 7:  # spread across speakers
                continue
            x, sr = sf.read(io.BytesIO(tf.extractfile(m).read()))
            out.append(norm(to16k(x, sr)))
            manifest["libri"].append(m.name)
            total += len(x) / sr
            if total >= minutes * 60:
                break
    x = np.concatenate([np.concatenate([a, np.zeros(int(0.4 * SR), np.float32)]) for a in out])
    np.save(cache, x)
    return x


# ---------- AMI ----------
def ami(minutes):
    cache = os.path.join(ROOT, "ami.npy")
    if os.path.exists(cache):
        return np.load(cache)
    from datasets import load_dataset, Audio
    ds = load_dataset("edinburghcstr/ami", "ihm", split="test", streaming=True)
    ds = ds.cast_column("audio", Audio(decode=False))
    out, total = [], 0
    for i, r in enumerate(ds):
        x, sr = sf.read(io.BytesIO(r["audio"]["bytes"]))
        x = to16k(x, sr)
        out.append(norm(x))
        out.append(np.zeros(int(0.15 * SR), np.float32))
        manifest["ami"].append(f'{r.get("meeting_id")}/{r.get("audio_id")}')
        total += len(x) / SR
        if total >= minutes * 60:
            break
    x = np.concatenate(out)
    np.save(cache, x)
    return x


# ---------- FMA ----------
def fma(n_tracks):
    cache = os.path.join(ROOT, "fma.npy")
    if os.path.exists(cache):
        return np.load(cache)
    from remotezip import RemoteZip
    import pandas as pd
    base = "https://os.unil.cloud.switch.ch/fma/"
    with RemoteZip(base + "fma_metadata.zip") as z:
        df = pd.read_csv(z.open("fma_metadata/tracks.csv"), header=[0, 1], index_col=0, low_memory=False)
    small = df[df[("set", "subset")] == "small"]
    cc = small[small[("track", "license")].astype(str).str.contains("Creative Commons", na=False)]
    by_genre = {}
    for tid, row in cc.iterrows():
        by_genre.setdefault(str(row[("track", "genre_top")]), []).append(tid)
    print("FMA CC tracks per genre:", {k: len(v) for k, v in by_genre.items()}, flush=True)
    picks = []
    genres = sorted(by_genre)
    for g in genres:
        rng.shuffle(by_genre[g])
    i = 0
    while len(picks) < n_tracks:
        g = genres[i % len(genres)]
        if by_genre[g]:
            picks.append(by_genre[g].pop())
        i += 1
    out = []
    with RemoteZip(base + "fma_small.zip") as z:
        for tid in picks:
            name = f"fma_small/{tid:06d}"[:-6] + f"{tid:06d}"
            name = f"fma_small/{tid // 1000:03d}/{tid:06d}.mp3"
            raw = z.read(name)
            p = subprocess.run(["ffmpeg", "-v", "error", "-i", "pipe:0", "-f", "f32le", "-ac", "1",
                                "-ar", str(SR), "pipe:1"], input=raw, capture_output=True)
            x = np.frombuffer(p.stdout, np.float32)
            if len(x) < 5 * SR:
                continue
            out.append(norm(x, -22.0))
            r = small.loc[tid]
            manifest["fma"].append({"track_id": int(tid), "genre": str(r[("track", "genre_top")]),
                                    "license": str(r[("track", "license")])})
    x = np.concatenate(out)
    np.save(cache, x)
    return x


def main():
    speech = libri(33.5)
    conv = ami(12)
    music = fma(70)  # ~35 min of 30 s clips; used for music + TV beds
    print("lengths (min): speech %.1f conv %.1f music %.1f" % (len(speech) / SR / 60, len(conv) / SR / 60, len(music) / SR / 60))
    sp_a, sp_tv = speech[: 20 * 60 * SR], speech[20 * 60 * SR:]
    mus_a, mus_bed = music[: 15 * 60 * SR], music[15 * 60 * SR:]
    n = min(len(sp_tv), 13 * 60 * SR, len(mus_bed))
    snr = 11.0
    tv = norm(sp_tv[:n]) * 1.0 + norm(mus_bed[:n]) * 10 ** (-snr / 20)
    tv = norm(tv)
    # interleave in ~2-minute blocks so the hour alternates like a normal evening
    parts = {"speech": sp_a, "conv": conv, "music": mus_a, "tv": tv}
    blocks, cursor = [], {k: 0 for k in parts}
    order = []
    for k, v in parts.items():
        order += [k] * int(len(v) / (120 * SR) + 0.999)
    rng.shuffle(order)
    for k in order:
        a = cursor[k]
        seg = parts[k][a: a + 120 * SR]
        cursor[k] += 120 * SR
        if len(seg):
            blocks.append(seg)
    hour = np.concatenate(blocks)
    sf.write(os.path.join(ROOT, "hour.wav"), hour, SR, subtype="PCM_16")
    manifest["hour_seconds"] = len(hour) / SR
    json.dump(manifest, open(os.path.join(ROOT, "manifest.json"), "w"), indent=1)
    print("wrote hour.wav, %.1f min" % (len(hour) / SR / 60))


if __name__ == "__main__":
    main()
