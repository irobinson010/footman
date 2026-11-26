#!/usr/bin/env python3
"""Tokenise wake/stage-2 phrases with the KWS model's BPE vocabulary.
Writes data/kw/wake.txt and data/kw/stage2.txt as lines: "<tokens> @<NAME>".
The Go harness appends per-sensitivity ':boost #threshold'."""
import os
import sentencepiece as spm

here = os.path.dirname(os.path.abspath(__file__))
m = os.path.join(here, "..", "data", "models", "sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01", "bpe.model")
sp = spm.SentencePieceProcessor(model_file=m)
out = os.path.join(here, "..", "data", "kw")
os.makedirs(out, exist_ok=True)
sets = {
    "wake": ["ALFRED", "JARVIS", "HEY FOOTMAN", "HEY JARVIS", "ALEXA", "HEY MYCROFT"],
    "stage2": ["STOP EVERYTHING", "CANCEL EVERYTHING"],
}
for k, phrases in sets.items():
    with open(os.path.join(out, k + ".txt"), "w") as f:
        for p in phrases:
            toks = " ".join(sp.encode(p, out_type=str))
            f.write(f"{toks} @{p.replace(' ', '_')}\n")
            print(k, p, "->", toks)
