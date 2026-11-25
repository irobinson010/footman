#!/usr/bin/env python3
"""Terminal sink: run inside a terminal emulator (konsole -e ttysink.py LOG).

Puts the tty in raw mode, enables bracketed paste, and logs everything that
arrives as JSON lines {"t": ms, "text": accumulated, "raw": chunk}. Bracketed
paste markers (ESC[200~ ... ESC[201~) are kept in "raw" and stripped from
"text", so the log shows whether a paste was bracketed. Ctrl+L clears the accumulated text.
"""
import json, os, sys, termios, time, tty

log = open(sys.argv[1], "a", buffering=1)
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
sys.stdout.write("\x1b[?2004h")  # bracketed paste on, like bash/zsh do
sys.stdout.write("ttysink ready\r\n")
sys.stdout.flush()
acc = ""
try:
    while True:
        b = os.read(fd, 65536)
        if not b:
            break
        chunk = b.decode("utf-8", "replace")
        if chunk == "\x0c":  # Ctrl+L resets the log between test runs
            acc = ""
            continue
        acc += chunk.replace("\x1b[200~", "").replace("\x1b[201~", "")
        log.write(json.dumps({"t": time.time() * 1000, "text": acc, "raw": chunk}) + "\n")
        sys.stdout.write(chunk.replace("\r", "\r\n"))
        sys.stdout.flush()
finally:
    termios.tcsetattr(fd, termios.TCSADRAIN, old)
