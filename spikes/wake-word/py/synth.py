#!/usr/bin/env python3
"""Synthesise positive/negative utterances on CPU (sherpa-onnx VITS/Piper, 2 threads)
and mix them into background excerpts at several SNRs.

Output: data/pos/<snr>/<id>.wav  (8 s, 16 kHz mono) and data/pos/index.json with
ground truth: phrase, voice, speed, speech onset/end (s), approximate wake-word end (s).
"""
import json, os, random, sys
import numpy as np, soundfile as sf
import sherpa_onnx
from scipy.signal import resample_poly

SR = 16000
ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "data")
M = os.path.join(ROOT, "models")
rng = np.random.default_rng(7)

PHRASES = {
    "alfred": "Alfred",
    "jarvis": "Jarvis",
    "hey_footman": "Hey Footman",
    "hey_jarvis": "Hey Jarvis",
    "alexa": "Alexa",
    "hey_mycroft": "Hey Mycroft",
    "alfred_stop": "Alfred, stop everything",
    "alfred_cancel": "Alfred, cancel everything",
    "stop": "Stop everything",       # break-glass WITHOUT wake word: must never fire
    "cancel": "Cancel everything",   # same
}
# multi-word phrase -> isolated wake-word text used to estimate the wake-word end
WAKE_PREFIX = {"alfred_stop": "Alfred", "alfred_cancel": "Alfred"}
SNRS = [None, 20, 10, 5, 0]  # None = clean (no background)
SPEEDS = [0.9, 1.1]          # length_scale: <1 faster

voices = []
for spk in [3, 60, 150, 250, 330, 420, 520, 640, 760, 880]:
    voices.append(("libritts_r", f"vits-piper-en_US-libritts_r-medium-int8", "en_US-libritts_r-medium.onnx", spk))
voices.append(("ryan", "vits-piper-en_US-ryan-medium-int8", "en_US-ryan-medium.onnx", 0))
voices.append(("amy", "vits-piper-en_US-amy-medium-int8", "en_US-amy-medium.onnx", 0))


def make_tts(d, onnx):
    p = os.path.join(M, d)
    cfg = sherpa_onnx.OfflineTtsConfig(
        model=sherpa_onnx.OfflineTtsModelConfig(
            vits=sherpa_onnx.OfflineTtsVitsModelConfig(
                model=os.path.join(p, onnx), tokens=os.path.join(p, "tokens.txt"),
                data_dir=os.path.join(p, "espeak-ng-data"), length_scale=1.0),
            num_threads=2, provider="cpu"))
    return sherpa_onnx.OfflineTts(cfg)


def gen(tts, text, sid, speed):
    a = tts.generate(text, sid=sid, speed=speed)
    x = np.array(a.samples, dtype=np.float32)
    if a.sample_rate != SR:
        from math import gcd
        g = gcd(a.sample_rate, SR)
        x = resample_poly(x, SR // g, a.sample_rate // g).astype(np.float32)
    return x


def trim(x, thr_db=-40):
    env = np.abs(x)
    k = int(0.01 * SR)
    env = np.convolve(env, np.ones(k) / k, "same")
    peak = env.max()
    idx = np.where(env > peak * 10 ** (thr_db / 20))[0]
    return x[idx[0]: idx[-1] + 1]


def main():
    hour = sf.read(os.path.join(ROOT, "bg", "hour.wav"), dtype="float32")[0]
    # background excerpts for the test: use the last 10 minutes of the hour file would reuse
    # false-wake audio; that is fine (speech-in-noise realism), a different offset per clip.
    index = []
    cache = {}
    n = 0
    for vi, (name, d, onnx, sid) in enumerate(voices):
        if d not in cache:
            cache[d] = make_tts(d, onnx)
        tts = cache[d]
        for pk, text in PHRASES.items():
            for sp in SPEEDS:
                speed = 1.0 / sp
                x = trim(gen(tts, text, sid, speed))
                x = x / (np.abs(x).max() + 1e-9) * 0.5
                wake_len = len(x)
                if pk in WAKE_PREFIX:
                    wake_len = len(trim(gen(tts, WAKE_PREFIX[pk], sid, speed)))
                clip_len = 8 * SR
                if len(x) > clip_len - 2 * SR:
                    continue
                onset = int(rng.uniform(1.5, 3.0) * SR)
                start = int(rng.integers(0, len(hour) - clip_len))
                bg = hour[start: start + clip_len].copy()
                rms_s = np.sqrt(np.mean(x ** 2))
                rms_b = np.sqrt(np.mean(bg ** 2)) + 1e-9
                for snr in SNRS:
                    clip = np.zeros(clip_len, np.float32)
                    clip[onset: onset + len(x)] += x
                    if snr is not None:
                        clip += bg * (rms_s / rms_b) * 10 ** (-snr / 20)
                    else:
                        clip += rng.normal(0, 1e-4, clip_len).astype(np.float32)  # near-digital silence
                    clip = np.clip(clip, -1, 1)
                    tag = "clean" if snr is None else f"snr{snr}"
                    od = os.path.join(ROOT, "pos", tag)
                    os.makedirs(od, exist_ok=True)
                    fn = f"{pk}_{name}{sid}_{sp}.wav"
                    sf.write(os.path.join(od, fn), clip, SR, subtype="PCM_16")
                    index.append(dict(file=f"pos/{tag}/{fn}", phrase=pk, text=text, voice=f"{name}{sid}",
                                      speed=speed, snr=tag, onset=onset / SR, end=(onset + len(x)) / SR,
                                      wake_end=(onset + wake_len) / SR))
                n += 1
        print("voice done", name, sid, n, flush=True)
    json.dump(index, open(os.path.join(ROOT, "pos", "index.json"), "w"), indent=0)
    print("clips:", len(index))


if __name__ == "__main__":
    main()
