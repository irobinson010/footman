#!/usr/bin/env python3
"""Qt (KDE-style) text sink: a focused QPlainTextEdit that logs every change
as JSON lines {"t": ms, "text": full text}. Usage: qtsink.py LOG"""
import json, sys, time
from PyQt6.QtWidgets import QApplication, QPlainTextEdit

log = open(sys.argv[1], "a", buffering=1)
app = QApplication(sys.argv[:1])
app.setDesktopFileName("org.footman.qtsink")
w = QPlainTextEdit()
w.setWindowTitle("qtsink")
w.resize(900, 600)
w.textChanged.connect(lambda: log.write(json.dumps({"t": time.time() * 1000, "text": w.toPlainText()}) + "\n"))
w.show()
w.activateWindow()
w.setFocus()
app.exec()
