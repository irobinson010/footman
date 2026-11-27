"""Write listen.html next to the rendered WAVs: one audio player per test
sentence (not a blind test; the owner's A/B page is a separate job).

usage: make_listen.py MODEL_DIR   (reads MODEL_DIR/wavs/NN.wav and sentences.txt)
"""
import html
import sys
from pathlib import Path

d = Path(sys.argv[1])
lines = [s.strip() for s in (Path(__file__).parent / "sentences.txt").read_text().splitlines()
         if s.strip() and not s.startswith("#")]
rows = "\n".join(
    f'<p>{i:02d}. {html.escape(t)}<br><audio controls preload="none" src="wavs/{i:02d}.wav"></audio></p>'
    for i, t in enumerate(lines, 1)
)
(d / "listen.html").write_text(
    f"<!doctype html><meta charset=utf-8><title>Ashford (Piper) test sentences</title>"
    f"<body style='font-family:sans-serif;max-width:50em;margin:2em auto'><h1>Ashford (Piper fine-tune)</h1>{rows}</body>\n"
)
