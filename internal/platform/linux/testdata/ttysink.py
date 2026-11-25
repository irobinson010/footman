#!/usr/bin/env python3
"""Terminal sink for the nested input tests; run inside a terminal emulator
(konsole -e ttysink.py LOG). Puts the tty in raw mode with bracketed paste on,
as shells do, and logs everything that arrives as JSON lines
{"text": accumulated text without paste markers, "raw": accumulated raw}."""
import json, os, sys, termios, tty

log = open(sys.argv[1], "a", buffering=1)
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
sys.stdout.write("\x1b[?2004h")
sys.stdout.write("ttysink ready\r\n")
sys.stdout.flush()
log.write(json.dumps({"ready": True, "text": "", "raw": ""}) + "\n")
raw = ""
try:
    while True:
        b = os.read(fd, 65536)
        if not b:
            break
        raw += b.decode("utf-8", "replace")
        text = raw.replace("\x1b[200~", "").replace("\x1b[201~", "")
        log.write(json.dumps({"text": text, "raw": raw}) + "\n")
finally:
    termios.tcsetattr(fd, termios.TCSADRAIN, old)
