// Package wl is a tiny, pure-Go Wayland wire-protocol client. It is just
// enough to bind a global and send requests that carry ints and strings (no
// fds), which is all org_kde_kwin_fake_input needs. Throwaway spike code.
package wl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

type Conn struct {
	c       *net.UnixConn
	nextID  uint32
	Globals []Global
	regID   uint32
}

func Dial() (*Conn, error) {
	disp := os.Getenv("WAYLAND_DISPLAY")
	if disp == "" {
		return nil, errors.New("WAYLAND_DISPLAY not set")
	}
	if !filepath.IsAbs(disp) {
		disp = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), disp)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: disp, Net: "unix"})
	if err != nil {
		return nil, err
	}
	w := &Conn{c: c, nextID: 2}
	w.regID = w.NewID()
	w.Send(1, 1, U(w.regID)) // wl_display.get_registry
	if err := w.Roundtrip(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Conn) NewID() uint32 { id := w.nextID; w.nextID++; return id }

// Arg encoders.
func U(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
func S(s string) []byte {
	n := len(s) + 1
	pad := (4 - n%4) % 4
	b := make([]byte, 4+n+pad)
	binary.LittleEndian.PutUint32(b, uint32(n))
	copy(b[4:], s)
	return b
}

func (w *Conn) Send(obj uint32, opcode uint16, args ...[]byte) error {
	size := 8
	for _, a := range args {
		size += len(a)
	}
	msg := make([]byte, 0, size)
	msg = append(msg, U(obj)...)
	msg = append(msg, U(uint32(size)<<16|uint32(opcode))...)
	for _, a := range args {
		msg = append(msg, a...)
	}
	_, err := w.c.Write(msg)
	return err
}

// Bind binds global g at version v and returns the new object id.
func (w *Conn) Bind(g Global, v uint32) uint32 {
	id := w.NewID()
	w.Send(w.regID, 0, U(g.Name), S(g.Interface), U(v), U(id))
	return id
}

func (w *Conn) Find(iface string) (Global, bool) {
	for _, g := range w.Globals {
		if g.Interface == iface {
			return g, true
		}
	}
	return Global{}, false
}

// Roundtrip sends wl_display.sync and reads events until the callback fires,
// collecting registry globals and surfacing protocol errors.
func (w *Conn) Roundtrip() error {
	cb := w.NewID()
	if err := w.Send(1, 0, U(cb)); err != nil {
		return err
	}
	hdr := make([]byte, 8)
	for {
		if _, err := readFull(w.c, hdr); err != nil {
			return err
		}
		obj := binary.LittleEndian.Uint32(hdr)
		so := binary.LittleEndian.Uint32(hdr[4:])
		size, op := int(so>>16), uint16(so&0xffff)
		body := make([]byte, size-8)
		if _, err := readFull(w.c, body); err != nil {
			return err
		}
		switch {
		case obj == 1 && op == 0: // wl_display.error
			code := binary.LittleEndian.Uint32(body[4:])
			msg := readStr(body[8:])
			return fmt.Errorf("wayland error on object %d code %d: %s", binary.LittleEndian.Uint32(body), code, msg)
		case obj == w.regID && op == 0: // wl_registry.global
			name := binary.LittleEndian.Uint32(body)
			iface := readStr(body[4:])
			n := binary.LittleEndian.Uint32(body[4:])
			off := 4 + 4 + int(n) + (4-int(n)%4)%4
			ver := binary.LittleEndian.Uint32(body[off:])
			w.Globals = append(w.Globals, Global{name, iface, ver})
		case obj == cb && op == 0: // wl_callback.done
			return nil
		}
	}
}

func readStr(b []byte) string {
	n := binary.LittleEndian.Uint32(b)
	if n == 0 {
		return ""
	}
	return string(b[4 : 4+n-1])
}

func readFull(c *net.UnixConn, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := c.Read(b[got:])
		if err != nil {
			return got, err
		}
		got += n
	}
	return got, nil
}
