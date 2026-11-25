#!/usr/bin/env python3
"""GTK 4 text sink: a focused TextView that logs every change as JSON lines
{"t": ms, "text": full text}. Usage: gtksink.py LOG"""
import json, sys, time
import gi
gi.require_version("Gtk", "4.0")
from gi.repository import Gtk

log = open(sys.argv[1], "a", buffering=1)

def on_activate(app):
    win = Gtk.ApplicationWindow(application=app, title="gtksink")
    win.set_default_size(900, 600)
    tv = Gtk.TextView()
    buf = tv.get_buffer()
    def changed(b):
        s, e = b.get_bounds()
        log.write(json.dumps({"t": time.time() * 1000, "text": b.get_text(s, e, True)}) + "\n")
    buf.connect("changed", changed)
    win.set_child(tv)
    win.present()
    tv.grab_focus()

app = Gtk.Application(application_id="org.footman.gtksink")
app.connect("activate", on_activate)
app.run([])
