"""Natural-pace wake-word positives for #94: wake word and command in one breath.

Usage: genpos.py PIPER_MODEL_DIR OUT_DIR
Writes <OUT>/<name>_s<spk>_<scale>.wav, 16 kHz mono, 0.3 s lead, trimmed at -40 dB.
"""
import os
import sys
from math import gcd

import numpy as np
import sherpa_onnx
import soundfile as sf
from scipy.signal import resample_poly

SR = 16000
p, out = sys.argv[1], sys.argv[2]
os.makedirs(out, exist_ok=True)
tts = sherpa_onnx.OfflineTts(sherpa_onnx.OfflineTtsConfig(model=sherpa_onnx.OfflineTtsModelConfig(
    vits=sherpa_onnx.OfflineTtsVitsModelConfig(
        model=os.path.join(p, "en_US-libritts_r-medium.onnx"), tokens=os.path.join(p, "tokens.txt"),
        data_dir=os.path.join(p, "espeak-ng-data"), length_scale=1.0),
    num_threads=4, provider="cpu")))

# Speakers not used by testdata/wake (3, 60, 150, 250, 330, 420).
SPEAKERS = [10, 25, 40, 75, 90, 110, 130, 170, 200, 230, 270, 300, 350, 380, 400, 450, 500, 550, 600, 650]
SCALES = [0.8, 1.0]  # length_scale: 0.8 is brisk, 1.0 the voice's normal pace
PHRASES = {
    "ashford": "Ashford, open YouTube.",
    "alfred": "Alfred, what time is it?",
    "hey_footman": "Hey Footman, pause the music.",
}
for name, text in PHRASES.items():
    for sid in SPEAKERS:
        for sc in SCALES:
            a = tts.generate(text, sid=sid, speed=1.0 / sc)
            x = np.asarray(a.samples, dtype=np.float32)
            if a.sample_rate != SR:
                g = gcd(a.sample_rate, SR)
                x = resample_poly(x, SR // g, a.sample_rate // g).astype(np.float32)
            k = int(0.02 * SR)
            env = np.convolve(np.abs(x), np.ones(k) / k, "same")
            idx = np.where(env > 10 ** (-40 / 20) * env.max())[0]
            x = x[max(0, idx[0] - k):idx[-1] + k]
            lead = (np.random.default_rng(sid).standard_normal(int(0.3 * SR)) * 1e-4).astype(np.float32)
            sf.write(os.path.join(out, f"{name}_s{sid}_{sc}.wav"), np.concatenate([lead, x]), SR, subtype="PCM_16")
print("done")
