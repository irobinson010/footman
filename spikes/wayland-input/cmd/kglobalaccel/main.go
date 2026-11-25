// kglobalaccel registers a push-to-talk shortcut directly with KDE's
// kglobalaccel daemon (hosted inside kwin_wayland in Plasma 6) over D-Bus, the
// same private API the KGlobalAccel library uses, and prints
// globalShortcutPressed / Released / Repeated. No dialog, no portal.
//
//	kglobalaccel [-component footman] [-key Pause] [-for 30s] [-unregister]
//
// Key codes are Qt key sequences: Qt::Key | modifier bits.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	svc   = "org.kde.kglobalaccel"
	iface = "org.kde.KGlobalAccel"
	comp  = "org.kde.kglobalaccel.Component"

	setPresent    = 2
	noAutoloading = 4
)

var qtKeys = map[string]int32{
	"pause": 0x01000008, "f9": 0x01000038, "f12": 0x0100003b, "space": 0x20, "p": 0x50,
	"scrolllock": 0x01000026, "f13": 0x0100003c,
}
var qtMods = map[string]int32{"shift": 0x02000000, "ctrl": 0x04000000, "alt": 0x08000000, "meta": 0x10000000}

func parse(s string) (int32, error) {
	var v int32
	for _, p := range strings.Split(strings.ToLower(s), "+") {
		if m, ok := qtMods[p]; ok {
			v |= m
		} else if k, ok := qtKeys[p]; ok {
			v |= k
		} else {
			return 0, fmt.Errorf("unknown key %q", p)
		}
	}
	return v, nil
}

type keySeq struct{ Keys []int32 } // QKeySequence on the wire: (ai)

func main() {
	component := flag.String("component", "footman-spike", "component unique name")
	action := flag.String("action", "push-to-talk", "action unique name")
	key := flag.String("key", "Pause", "shortcut, e.g. Pause or ctrl+shift+space")
	dur := flag.Duration("for", 30*time.Second, "how long to listen")
	unreg := flag.Bool("unregister", false, "remove the action and exit")
	flag.Parse()

	c, err := dbus.ConnectSessionBus()
	check(err)
	ka := c.Object(svc, "/kglobalaccel")
	id := []string{*component, *action, "Footman (spike)", "Push to talk"}
	if *unreg {
		var ok bool
		check(ka.Call(iface+".unregister", 0, *component, *action).Store(&ok))
		fmt.Println("unregistered:", ok)
		return
	}
	k, err := parse(*key)
	check(err)
	check(ka.Call(iface+".doRegister", 0, id).Err)
	var got []keySeq
	check(ka.Call(iface+".setShortcutKeys", 0, id, []keySeq{{[]int32{k, 0, 0, 0}}}, uint32(setPresent|noAutoloading)).Store(&got))
	fmt.Printf("requested %s (0x%08x), kglobalaccel assigned %v\n", *key, k, got)
	if len(got) == 0 || len(got[0].Keys) == 0 || got[0].Keys[0] == 0 {
		fmt.Println("shortcut not assigned (taken by another component?)")
	}
	var path dbus.ObjectPath
	check(ka.Call(iface+".getComponent", 0, *component).Store(&path))
	check(c.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface(comp)))
	sigs := make(chan *dbus.Signal, 16)
	c.Signal(sigs)
	fmt.Println("listening on", path)
	end := time.After(*dur)
	for {
		select {
		case s := <-sigs:
			if strings.HasPrefix(s.Name, comp+".globalShortcut") {
				fmt.Printf("%s %-24s %v %v ts=%v\n", time.Now().Format("15:04:05.000"), s.Name[len(comp)+1:], s.Body[0], s.Body[1], s.Body[2])
			}
		case <-end:
			// Mark inactive so the key goes back to apps while we are not running.
			ka.Call(iface+".setInactive", 0, id)
			return
		}
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
