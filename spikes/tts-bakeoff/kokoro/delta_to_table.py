"""delta_eN.pt (from train_style.py) -> 510x256 table: the init voice's pack plus the fitted offset.
usage: python delta_to_table.py DELTA.pt TABLE.pt"""
import sys

import torch

from common import load_voice_table

d = torch.load(sys.argv[1])
torch.save(load_voice_table(d["init"]) + d["delta"], sys.argv[2])
print(f"{sys.argv[2]}: init {d['init']}, epoch {d['epoch']}")
