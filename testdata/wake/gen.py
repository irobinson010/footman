#!/usr/bin/env python3
"""Regenerates the wake-word test clips in this folder (not run by the tests).

Usage: gen.py PIPER_MODEL_DIR   (vits-piper-en_US-libritts_r-medium-int8 from the
sherpa-onnx tts-models release; needs sherpa-onnx==1.13.8, numpy, scipy, soundfile).

Each clip is 16 kHz mono 16-bit: 0.3 s of near-silence, then the phrase trimmed
at -40 dB. Voices are LibriTTS-R speakers (CC BY 4.0), two speaking rates each.
"""
import os
import sys
from math import gcd

import numpy as np
import sherpa_onnx
import soundfile as sf
from scipy.signal import resample_poly

SR = 16000
here = os.path.dirname(os.path.abspath(__file__))
p = sys.argv[1]
tts = sherpa_onnx.OfflineTts(sherpa_onnx.OfflineTtsConfig(model=sherpa_onnx.OfflineTtsModelConfig(
    vits=sherpa_onnx.OfflineTtsVitsModelConfig(
        model=os.path.join(p, "en_US-libritts_r-medium.onnx"), tokens=os.path.join(p, "tokens.txt"),
        data_dir=os.path.join(p, "espeak-ng-data"), length_scale=1.0),
    num_threads=2, provider="cpu")))

SPEAKERS = [3, 60, 150, 250, 330, 420]
SPEEDS = [0.9, 1.1]  # length_scale: below 1 is faster
# name -> (text, number of speakers)
PHRASES = {
    "alfred": ("Alfred", 6), "hey_footman": ("Hey Footman", 6),
    "alfred_stop": ("Alfred, stop everything", 6), "alfred_cancel": ("Alfred, cancel everything", 6),
    "stop": ("Stop everything", 4), "cancel": ("Cancel everything", 4),
    "jarvis": ("Jarvis", 4), "alexa": ("Alexa", 4), "hey_mycroft": ("Hey Mycroft", 4),
    "talk1": ("Good morning, could you pass me the salt please", 4),
    "talk2": ("I think we should stop and cancel the meeting everything is fine", 4),
}
rng = np.random.default_rng(7)
for name, (text, nspk) in PHRASES.items():
    for sid in SPEAKERS[:nspk]:
        for sp in SPEEDS:
            a = tts.generate(text, sid=sid, speed=1.0 / sp)
            x = np.array(a.samples, dtype=np.float32)
            g = gcd(a.sample_rate, SR)
            x = resample_poly(x, SR // g, a.sample_rate // g).astype(np.float32)
            k = int(0.01 * SR)
            env = np.convolve(np.abs(x), np.ones(k) / k, "same")
            idx = np.where(env > env.max() * 10 ** (-40 / 20))[0]
            x = x[idx[0]: idx[-1] + 1]
            x = x / (np.abs(x).max() + 1e-9) * 0.5
            lead = rng.normal(0, 1e-4, int(0.3 * SR)).astype(np.float32)
            sf.write(os.path.join(here, f"{name}_s{sid}_{sp}.wav"), np.concatenate([lead, x]), SR, subtype="PCM_16")
