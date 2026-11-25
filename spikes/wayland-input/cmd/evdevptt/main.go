// evdevptt watches every keyboard under /dev/input for one key and prints
// press / repeat / release with timestamps. Pure Go, read-only, no grab: the
// key still reaches the focused app as normal.
//
// Needs read access to /dev/input/event* (group "input" on Ubuntu). That means
// the process can read every keystroke on the machine, in every session and at
// the lock screen, which is why Footman should not need it by default.
//
//	evdevptt [-key 70] [-for 30s]     (70 = KEY_SCROLLLOCK, 119 = KEY_PAUSE)
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	evKey   = 0x01
	keyA    = 30
	keyMax  = 0x2ff
	evMax   = 0x1f
	evSize  = 24 // struct input_event on 64-bit: timeval(16) + type(2) + code(2) + value(4)
	iocRead = 2
)

func eviocgbit(ev, n uint) uintptr {
	// _IOC(_IOC_READ, 'E', 0x20 + ev, n)
	return uintptr(iocRead<<30 | n<<16 | uint('E')<<8 | (0x20 + ev))
}

func eviocgname(n uint) uintptr { return uintptr(iocRead<<30 | n<<16 | uint('E')<<8 | 0x06) }

func hasBit(b []byte, bit int) bool { return b[bit/8]&(1<<(bit%8)) != 0 }

func main() {
	key := flag.Int("key", 70, "evdev key code to watch (70 ScrollLock, 119 Pause, 183 F13)")
	dur := flag.Duration("for", 30*time.Second, "how long to listen")
	flag.Parse()

	paths, _ := filepath.Glob("/dev/input/event*")
	type dev struct {
		f    *os.File
		name string
	}
	events := make(chan string, 64)
	opened := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			continue
		}
		keys := make([]byte, keyMax/8+1)
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), eviocgbit(evKey, uint(len(keys))), uintptr(unsafe.Pointer(&keys[0]))); e != 0 || !hasBit(keys, *key) {
			f.Close()
			continue
		}
		name := make([]byte, 256)
		unix.Syscall(unix.SYS_IOCTL, f.Fd(), eviocgname(uint(len(name))), uintptr(unsafe.Pointer(&name[0])))
		d := dev{f, string(name[:clen(name)])}
		opened++
		fmt.Printf("watching %s (%s)\n", p, d.name)
		go func() {
			buf := make([]byte, evSize*64)
			for {
				n, err := d.f.Read(buf)
				if err != nil {
					return
				}
				for off := 0; off+evSize <= n; off += evSize {
					typ := binary.LittleEndian.Uint16(buf[off+16:])
					code := binary.LittleEndian.Uint16(buf[off+18:])
					val := int32(binary.LittleEndian.Uint32(buf[off+20:]))
					if typ == evKey && int(code) == *key {
						state := map[int32]string{0: "release", 1: "press", 2: "repeat"}[val]
						events <- fmt.Sprintf("%s %-7s (%s)", time.Now().Format("15:04:05.000"), state, d.name)
					}
				}
			}
		}()
	}
	if opened == 0 {
		fmt.Fprintln(os.Stderr, "no readable keyboard has that key; are you in the input group?")
		os.Exit(1)
	}
	end := time.After(*dur)
	for {
		select {
		case e := <-events:
			fmt.Println(e)
		case <-end:
			return
		}
	}
}

func clen(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return len(b)
}
