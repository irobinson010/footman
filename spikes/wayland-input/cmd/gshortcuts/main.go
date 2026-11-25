// gshortcuts binds a push-to-talk shortcut through the XDG GlobalShortcuts
// portal (pure Go, godbus) and prints Activated / Deactivated (press /
// release) signals with timestamps.
//
//	gshortcuts [-app org.footman.spike.gshortcuts] [-trigger CTRL+ALT+F9] [-for 30s]
//
// On KDE the first BindShortcuts for a new shortcut id shows a dialog; the
// binding is then stored in kglobalshortcutsrc under the app id (or under
// "token_<session token>" when the caller has no app id).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/spikes/wayland-input/internal/portal"
)

const gs = "org.freedesktop.portal.GlobalShortcuts"

func main() {
	app := flag.String("app", "", "register this app id with the host Registry first")
	trigger := flag.String("trigger", "CTRL+ALT+F9", "preferred_trigger (XDG shortcuts spec)")
	id := flag.String("id", "push-to-talk", "shortcut id")
	sessTok := flag.String("session-token", "footman", "session_handle_token (stable => stable component name without app id)")
	dur := flag.Duration("for", 30*time.Second, "how long to listen")
	timeout := flag.Duration("timeout", 60*time.Second, "how long to wait for the bind dialog")
	flag.Parse()

	p, err := portal.Connect()
	check(err)
	if *app != "" {
		check(p.Register(*app))
	}
	code, res, err := p.Request(gs+".CreateSession", 10*time.Second, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(*sessTok),
	})
	check(err)
	if code != 0 {
		fail("CreateSession: %d", code)
	}
	session := dbus.ObjectPath(res["session_handle"].Value().(string))

	type shortcut struct {
		ID   string
		Opts map[string]dbus.Variant
	}
	shortcuts := []shortcut{{*id, map[string]dbus.Variant{
		"description":       dbus.MakeVariant("Footman push-to-talk"),
		"preferred_trigger": dbus.MakeVariant(*trigger),
	}}}
	check(p.Conn.AddMatchSignal(dbus.WithMatchInterface(gs)))
	t0 := time.Now()
	code, res, err = p.Request(gs+".BindShortcuts", *timeout, session, shortcuts, "", map[string]dbus.Variant{})
	check(err)
	fmt.Printf("BindShortcuts response=%d after %v: %v\n", code, time.Since(t0).Round(time.Millisecond), res["shortcuts"])
	if code != 0 {
		os.Exit(1)
	}
	fmt.Println("listening; press and hold the shortcut")
	end := time.After(*dur)
	for {
		select {
		case s := <-p.Signals():
			if s.Name == gs+".Activated" || s.Name == gs+".Deactivated" {
				now := time.Now()
				// Body: session, shortcut id, timestamp (ms, compositor clock), options
				fmt.Printf("%s %-11s id=%v ts=%v\n", now.Format("15:04:05.000"), s.Name[len(gs)+1:], s.Body[1], s.Body[2])
			}
		case <-end:
			return
		}
	}
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+f+"\n", a...)
	os.Exit(1)
}
