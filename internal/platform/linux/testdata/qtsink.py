#!/usr/bin/env python3
"""Qt text sink for the nested input tests: a focused QPlainTextEdit that
logs every change as a JSON line {"text": full text, "t": Unix time in
seconds}. Usage: qtsink.py LOG"""
import json, sys, time
from PyQt6.QtWidgets import QApplication, QPlainTextEdit

log = open(sys.argv[1], "a", buffering=1)
app = QApplication(sys.argv[:1])
app.setDesktopFileName("org.footman.qtsink")
w = QPlainTextEdit()
w.setWindowTitle("qtsink")
w.resize(900, 600)
w.textChanged.connect(lambda: log.write(json.dumps({"text": w.toPlainText(), "t": time.time()}) + "\n"))
w.show()
w.activateWindow()
w.setFocus()
log.write(json.dumps({"ready": True, "text": ""}) + "\n")
app.exec()
