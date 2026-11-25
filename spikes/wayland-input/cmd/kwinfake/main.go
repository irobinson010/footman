// kwinfake types text or presses key combos through KWin's
// org_kde_kwin_fake_input Wayland protocol, in pure Go.
//
// KWin only exposes that global to executables whose .desktop file lists it in
// X-KDE-Wayland-Interfaces (see install-desktop.sh), so -list is the quickest
// way to see whether this binary is authorised.
//
//	kwinfake -list
//	kwinfake -type 'hello wörld 👋' [-delay 0] [-sync 1]
//	kwinfake -combo ctrl+shift+v
//	kwinfake -hold meta+f9 -for 800ms
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/irobinson010/footman/spikes/wayland-input/internal/keys"
	"github.com/irobinson010/footman/spikes/wayland-input/internal/wl"
)

const (
	opAuthenticate   = 0
	opKeyboardKey    = 10
	opKeyboardKeysym = 12
	pressed          = 1
	released         = 0
)

func main() {
	list := flag.Bool("list", false, "list Wayland globals and exit")
	text := flag.String("type", "", "text to type via keyboard_keysym")
	file := flag.String("file", "", "read text to type from file")
	delay := flag.Duration("delay", 0, "delay between characters")
	syncEvery := flag.Int("sync", 1, "roundtrip to the compositor every N characters (0 = only at end)")
	combo := flag.String("combo", "", "press and release a key combo, e.g. ctrl+v")
	hold := flag.String("hold", "", "press a combo, wait -for, release")
	holdFor := flag.Duration("for", 500*time.Millisecond, "hold duration for -hold")
	fwd := flag.Bool("release-forward", false, "release keys in press order (modifiers first), like a person letting go of Ctrl before the key")
	idle := flag.Bool("idle", false, "stay connected forever (keeps a keyboard on the seat in a headless session)")
	flag.Parse()

	c, err := wl.Dial()
	check(err)
	if *list {
		for _, g := range c.Globals {
			fmt.Printf("%-45s v%d\n", g.Interface, g.Version)
		}
		return
	}
	g, ok := c.Find("org_kde_kwin_fake_input")
	if !ok {
		fmt.Fprintln(os.Stderr, "org_kde_kwin_fake_input not offered to this client (not authorised by a .desktop file?)")
		os.Exit(2)
	}
	v := min(g.Version, 6)
	fi := c.Bind(g, v)
	check(c.Send(fi, opAuthenticate, wl.S("footman-spike"), wl.S("dictation")))

	key := func(code, state uint32) { check(c.Send(fi, opKeyboardKey, wl.U(code), wl.U(state))) }

	switch {
	case *idle:
		check(c.Roundtrip())
		select {}
	case *combo != "" || *hold != "":
		s := *combo
		if *hold != "" {
			s = *hold
		}
		codes, err := keys.Combo(s)
		check(err)
		for _, k := range codes {
			key(k, pressed)
		}
		check(c.Roundtrip())
		if *hold != "" {
			time.Sleep(*holdFor)
		}
		if *fwd {
			for _, k := range codes {
				key(k, released)
				check(c.Roundtrip())
				time.Sleep(100 * time.Millisecond)
			}
		} else {
			for i := len(codes) - 1; i >= 0; i-- {
				key(codes[i], released)
			}
		}
		check(c.Roundtrip())
	case *text != "" || *file != "":
		if v < 6 {
			fmt.Fprintln(os.Stderr, "compositor fake_input < v6, no keyboard_keysym")
			os.Exit(2)
		}
		s := *text
		if *file != "" {
			b, err := os.ReadFile(*file)
			check(err)
			s = string(b)
		}
		start := time.Now()
		n := 0
		for _, r := range s {
			ks := keys.Keysym(r)
			check(c.Send(fi, opKeyboardKeysym, wl.U(ks), wl.U(pressed)))
			check(c.Send(fi, opKeyboardKeysym, wl.U(ks), wl.U(released)))
			n++
			if *syncEvery > 0 && n%*syncEvery == 0 {
				check(c.Roundtrip())
			}
			if *delay > 0 {
				time.Sleep(*delay)
			}
		}
		check(c.Roundtrip())
		el := time.Since(start)
		fmt.Fprintf(os.Stderr, "sent %d chars in %v (%.0f chars/s, sender side)\n", n, el, float64(n)/el.Seconds())
	default:
		flag.Usage()
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
