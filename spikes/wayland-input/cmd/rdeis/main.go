// rdeis types ASCII through the RemoteDesktop portal's ConnectToEIS fd using
// libei (cgo). Ubuntu ships libei.so.1 but not the headers (libei-dev), so the
// few functions used are declared here by hand from libei 1.5's libei.h.
//
// libei only sends evdev keycodes; the keymap belongs to the EIS side (KWin's
// current layout) and a client cannot change it, so characters that are not on
// the layout cannot be typed. This spike maps ASCII to US-layout keycodes.
//
//	rdeis [-app ID] [-token-file F] -file sample.txt
package main

/*
#cgo LDFLAGS: -l:libei.so.1
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <poll.h>

struct ei; struct ei_event; struct ei_seat; struct ei_device;
struct ei *ei_new_sender(void *user_data);
void ei_configure_name(struct ei *ei, const char *name);
int ei_setup_backend_fd(struct ei *ei, int fd);
int ei_get_fd(struct ei *ei);
void ei_dispatch(struct ei *ei);
struct ei_event *ei_get_event(struct ei *ei);
int ei_event_get_type(struct ei_event *event);
struct ei_seat *ei_event_get_seat(struct ei_event *event);
struct ei_device *ei_event_get_device(struct ei_event *event);
struct ei_event *ei_event_unref(struct ei_event *event);
void ei_seat_bind_capabilities(struct ei_seat *seat, ...);
bool ei_device_has_capability(struct ei_device *device, int cap);
struct ei_device *ei_device_ref(struct ei_device *device);
void ei_device_start_emulating(struct ei_device *device, uint32_t sequence);
void ei_device_stop_emulating(struct ei_device *device);
void ei_device_keyboard_key(struct ei_device *device, uint32_t keycode, bool is_press);
void ei_device_frame(struct ei_device *device, uint64_t time);
uint64_t ei_now(struct ei *ei);

#define EI_DEVICE_CAP_KEYBOARD (1 << 2)
static void bind_keyboard(struct ei_seat *s) { ei_seat_bind_capabilities(s, EI_DEVICE_CAP_KEYBOARD, NULL); }
static int wait_fd(int fd, int ms) { struct pollfd p = { .fd = fd, .events = POLLIN }; return poll(&p, 1, ms); }
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/spikes/wayland-input/internal/portal"
)

const (
	evConnect       = 1
	evDisconnect    = 2
	evSeatAdded     = 3
	evDeviceAdded   = 5
	evDeviceResumed = 8
)

// US layout: rune -> (evdev keycode, shift)
var us = map[rune][2]int{}

func init() {
	rows := []struct {
		chars, shifted string
		codes          []int
	}{
		{"1234567890-=", "!@#$%^&*()_+", []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}},
		{"qwertyuiop[]", "QWERTYUIOP{}", []int{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27}},
		{"asdfghjkl;'`", "ASDFGHJKL:\"~", []int{30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41}},
		{"\\zxcvbnm,./", "|ZXCVBNM<>?", []int{43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53}},
	}
	for _, r := range rows {
		for i, c := range r.chars {
			us[c] = [2]int{r.codes[i], 0}
		}
		for i, c := range r.shifted {
			us[c] = [2]int{r.codes[i], 1}
		}
	}
	us[' '] = [2]int{57, 0}
	us['\n'] = [2]int{28, 0}
	us['\t'] = [2]int{15, 0}
}

func main() {
	app := flag.String("app", "", "register this app id with the host Registry first")
	tokenFile := flag.String("token-file", "", "restore token file")
	file := flag.String("file", "", "file with text to type")
	text := flag.String("type", "", "text to type")
	timeout := flag.Duration("timeout", 60*time.Second, "approval timeout")
	batch := flag.Int("batch", 8, "characters per burst")
	pause := flag.Duration("pause", time.Millisecond, "pause between bursts")
	flag.Parse()
	if *file == "" && flag.NArg() > 0 {
		*file = flag.Arg(0)
	}
	s := *text
	if *file != "" {
		b, err := os.ReadFile(*file)
		check(err)
		s = string(b)
	}

	t0 := time.Now()
	p, err := portal.Connect()
	check(err)
	if *app != "" {
		check(p.Register(*app))
	}
	session, _, err := p.StartRemoteDesktop(*tokenFile, *timeout)
	check(err)
	var fd dbus.UnixFD
	check(p.Obj.Call("org.freedesktop.portal.RemoteDesktop.ConnectToEIS", 0, session, map[string]dbus.Variant{}).Store(&fd))

	ei := C.ei_new_sender(nil)
	name := C.CString("footman-spike")
	C.ei_configure_name(ei, name)
	if rc := C.ei_setup_backend_fd(ei, C.int(fd)); rc != 0 {
		fail("ei_setup_backend_fd: %d", rc)
	}
	efd := C.ei_get_fd(ei)
	var kbd *C.struct_ei_device
	deadline := time.Now().Add(5 * time.Second)
	for kbd == nil {
		C.wait_fd(efd, 100)
		C.ei_dispatch(ei)
		for ev := C.ei_get_event(ei); ev != nil; ev = C.ei_get_event(ei) {
			switch C.ei_event_get_type(ev) {
			case evSeatAdded:
				C.bind_keyboard(C.ei_event_get_seat(ev))
			case evDeviceResumed:
				d := C.ei_event_get_device(ev)
				if C.ei_device_has_capability(d, 1<<2) {
					kbd = C.ei_device_ref(d)
				}
			case evDisconnect:
				fail("EIS disconnected")
			}
			C.ei_event_unref(ev)
		}
		if time.Now().After(deadline) {
			fail("no keyboard device from EIS")
		}
	}
	fmt.Fprintf(os.Stderr, "EIS keyboard ready in %v\n", time.Since(t0).Round(time.Millisecond))
	C.ei_device_start_emulating(kbd, 1)
	key := func(code int, down bool) {
		C.ei_device_keyboard_key(kbd, C.uint32_t(code), C.bool(down))
		C.ei_device_frame(kbd, C.ei_now(ei))
	}
	start, n, skipped := time.Now(), 0, 0
	for _, r := range s {
		k, ok := us[r]
		if !ok {
			skipped++
			continue
		}
		if k[1] == 1 {
			key(42, true)
		}
		key(k[0], true)
		key(k[0], false)
		if k[1] == 1 {
			key(42, false)
		}
		n++
		C.ei_dispatch(ei)
		// libei queues writes when the socket is full; without pacing the
		// tail of a long burst was still queued when the session closed.
		if n%*batch == 0 {
			time.Sleep(*pause)
			C.ei_dispatch(ei)
		}
	}
	C.ei_device_stop_emulating(kbd)
	for i := 0; i < 20; i++ {
		C.ei_dispatch(ei)
		time.Sleep(10 * time.Millisecond)
	}
	el := time.Since(start)
	fmt.Fprintf(os.Stderr, "sent %d chars (%d not on the layout, skipped) in %v (%.0f chars/s, sender side)\n", n, skipped, el, float64(n)/el.Seconds())
	time.Sleep(300 * time.Millisecond)
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
