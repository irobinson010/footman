// rdportal types text through the xdg-desktop-portal RemoteDesktop interface,
// pure Go over D-Bus: NotifyKeyboardKeysym per character (KDE forwards these
// to KWin's fake-input keysym path, so any Unicode keysym works), or a key
// combo via NotifyKeyboardKeycode. -eis only opens the session and prints the
// EIS fd number to show ConnectToEIS works (see cmd/rdeis for libei typing).
//
//	rdportal [-app org.footman.spike.rdportal] [-token-file F] -file sample.txt
//	rdportal -combo ctrl+shift+v
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/spikes/wayland-input/internal/keys"
	"github.com/irobinson010/footman/spikes/wayland-input/internal/portal"
)

const rd = "org.freedesktop.portal.RemoteDesktop"

func main() {
	app := flag.String("app", "", "register this app id with the host Registry first")
	tokenFile := flag.String("token-file", "", "load/save the restore token here (persist_mode=2)")
	text := flag.String("type", "", "text to type")
	file := flag.String("file", "", "file with text to type")
	combo := flag.String("combo", "", "key combo via NotifyKeyboardKeycode")
	delay := flag.Duration("delay", 0, "delay between characters")
	timeout := flag.Duration("timeout", 60*time.Second, "how long to wait for the approval dialog")
	flag.Parse()
	if len(flag.Args()) > 0 && *file == "" {
		*file = flag.Arg(0)
	}

	t0 := time.Now()
	p, err := portal.Connect()
	check(err)
	if *app != "" {
		check(p.Register(*app))
	}
	code, res, err := p.Request(rd+".CreateSession", 10*time.Second, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(portal.Token("s")),
	})
	check(err)
	if code != 0 {
		fail("CreateSession response %d", code)
	}
	session := dbus.ObjectPath(res["session_handle"].Value().(string))

	sel := map[string]dbus.Variant{
		"types":        dbus.MakeVariant(uint32(1)), // keyboard
		"persist_mode": dbus.MakeVariant(uint32(2)), // until revoked
	}
	if *tokenFile != "" {
		if b, err := os.ReadFile(*tokenFile); err == nil && len(b) > 0 {
			sel["restore_token"] = dbus.MakeVariant(strings.TrimSpace(string(b)))
		}
	}
	code, _, err = p.Request(rd+".SelectDevices", 10*time.Second, session, sel)
	check(err)
	if code != 0 {
		fail("SelectDevices response %d", code)
	}
	tStart := time.Now()
	code, res, err = p.Request(rd+".Start", *timeout, session, "", map[string]dbus.Variant{})
	check(err)
	if code != 0 {
		fail("Start response %d (1 = user cancelled, 2 = other)", code)
	}
	fmt.Fprintf(os.Stderr, "session ready in %v (Start took %v) devices=%v\n", time.Since(t0).Round(time.Millisecond), time.Since(tStart).Round(time.Millisecond), res["devices"])
	if tok, ok := res["restore_token"]; ok && *tokenFile != "" {
		check(os.WriteFile(*tokenFile, []byte(tok.Value().(string)), 0o600))
	}
	opts := map[string]dbus.Variant{}
	call := func(m string, args ...any) { check(p.Obj.Call(rd+"."+m, 0, args...).Err) }

	switch {
	case *combo != "":
		codes, err := keys.Combo(*combo)
		check(err)
		for _, k := range codes {
			call("NotifyKeyboardKeycode", session, opts, int32(k), uint32(1))
		}
		for i := len(codes) - 1; i >= 0; i-- {
			call("NotifyKeyboardKeycode", session, opts, int32(codes[i]), uint32(0))
		}
	case *text != "" || *file != "":
		s := *text
		if *file != "" {
			b, err := os.ReadFile(*file)
			check(err)
			s = string(b)
		}
		start, n := time.Now(), 0
		for _, r := range s {
			ks := int32(keys.Keysym(r))
			call("NotifyKeyboardKeysym", session, opts, ks, uint32(1))
			call("NotifyKeyboardKeysym", session, opts, ks, uint32(0))
			n++
			if *delay > 0 {
				time.Sleep(*delay)
			}
		}
		el := time.Since(start)
		fmt.Fprintf(os.Stderr, "sent %d chars in %v (%.0f chars/s, sender side)\n", n, el, float64(n)/el.Seconds())
	}
	// Give KDE's portal a moment to forward the last events before the session closes.
	time.Sleep(200 * time.Millisecond)
	p.Conn.Object(portal.Dest, session).Call("org.freedesktop.portal.Session.Close", 0)
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
