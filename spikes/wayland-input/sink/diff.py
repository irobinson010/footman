#!/usr/bin/env python3
"""Show exactly how a sink's final text differs from the expected text.
Usage: diff.py LOG EXPECTED_FILE [SINCE_MS]"""
import difflib, json, sys

since = float(sys.argv[3]) if len(sys.argv) > 3 else 0
recs = []
for line in open(sys.argv[1]):
    line = line.strip()
    if not line:
        continue
    r = json.loads(line)
    if max(r.get("t", 0), r.get("rt", 0)) >= since:
        recs.append(r)
recs.sort(key=lambda r: (r.get("seq", 0), r.get("t", 0)))
want = open(sys.argv[2]).read()
got = recs[-1]["text"].replace("\r", "\n")
print(f"want {len(want)} chars, got {len(got)} chars, {len(recs)} records")
for op in difflib.SequenceMatcher(None, want, got, autojunk=False).get_opcodes():
    if op[0] != "equal":
        print(f"  {op[0]:8} want[{op[1]}:{op[2]}]={want[op[1]:op[2]]!r} got[{op[3]}:{op[4]}]={got[op[3]:op[4]]!r}")
