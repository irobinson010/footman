"""Score every stock English Kokoro voice against the Ashford clips (mean multi-res log-mel L1 with
DTW-derived durations, the same loss train_style.py minimises) to choose the starting voice.
usage: python rank_voices.py [N_LINES]"""
import sys

from fit_lib import rank_voices

rank_voices(int(sys.argv[1]) if len(sys.argv) > 1 else 20)
