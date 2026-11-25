//go:build linux

package linux

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeCompositor is a tiny Wayland server for unit tests: a seat with a
// keyboard (sending a fixed keymap) and the ext_data_control_v1 clipboard.
// It also plays "the other apps": one that copied something (external) and
// the focused app that pastes (appRead).
type fakeCompositor struct {
	t      *testing.T
	socket string
	keymap string

	mu       sync.Mutex
	conn     *net.UnixConn
	ifaces   map[uint32]string // client object id -> interface
	sources  map[uint32][]string
	device   uint32
	nextSrv  uint32
	offers   map[uint32]uint32 // server offer id -> source id (0 = external)
	selSrc   uint32            // client source owning the selection, 0 if none
	external clip              // the selection when another app owns it
	reads    []string          // MIME types the focused app or anyone read from our client
	wmu      sync.Mutex
}

func newFakeCompositor(t *testing.T, keymap string) *fakeCompositor {
	t.Helper()
	dir, err := os.MkdirTemp("", "fmwl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeCompositor{t: t, socket: filepath.Join(dir, "wl"), keymap: keymap,
		ifaces: map[uint32]string{}, sources: map[uint32][]string{}, offers: map[uint32]uint32{}, nextSrv: 0xff000000}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.AcceptUnix()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conn = c
			f.ifaces = map[uint32]string{1: "wl_display"}
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeCompositor) send(obj uint32, op uint16, fd int, args ...[]byte) {
	size := 8
	for _, a := range args {
		size += len(a)
	}
	msg := make([]byte, 8, size)
	binary.LittleEndian.PutUint32(msg, obj)
	binary.LittleEndian.PutUint32(msg[4:], uint32(size)<<16|uint32(op))
	for _, a := range args {
		msg = append(msg, a...)
	}
	var oob []byte
	if fd >= 0 {
		oob = syscall.UnixRights(fd)
	}
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _, _ = c.WriteMsgUnix(msg, oob, nil)
}

func (f *fakeCompositor) serve(c *net.UnixConn) {
	var buf []byte
	var fds []int
	chunk := make([]byte, 4096)
	oob := make([]byte, 256)
	for {
		n, oobn, _, _, err := c.ReadMsgUnix(chunk, oob)
		if oobn > 0 {
			msgs, _ := syscall.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				got, _ := syscall.ParseUnixRights(&m)
				fds = append(fds, got...)
			}
		}
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 8 {
			obj := binary.LittleEndian.Uint32(buf)
			so := binary.LittleEndian.Uint32(buf[4:])
			size, op := int(so>>16), uint16(so&0xffff)
			if len(buf) < size {
				break
			}
			body := buf[8:size]
			buf = buf[size:]
			takeFD := func() int { fd := fds[0]; fds = fds[1:]; return fd }
			f.request(obj, op, body, takeFD)
		}
		if err != nil {
			return
		}
	}
}

func (f *fakeCompositor) request(obj uint32, op uint16, body []byte, fd func() int) {
	r := wlReader{b: body}
	f.mu.Lock()
	iface := f.ifaces[obj]
	f.mu.Unlock()
	switch iface {
	case "wl_display":
		switch op {
		case 0: // sync
			cb := r.uint()
			f.send(cb, 0, -1, wlUint(0))
			f.send(1, 1, -1, wlUint(cb))
		case 1: // get_registry
			reg := r.uint()
			f.mu.Lock()
			f.ifaces[reg] = "wl_registry"
			f.mu.Unlock()
			f.send(reg, 0, -1, wlUint(1), wlString("wl_seat"), wlUint(7))
			f.send(reg, 0, -1, wlUint(2), wlString("ext_data_control_manager_v1"), wlUint(1))
		}
	case "wl_registry": // bind
		r.uint()
		name := r.str()
		r.uint()
		id := r.uint()
		f.mu.Lock()
		f.ifaces[id] = name
		f.mu.Unlock()
	case "wl_seat":
		if op == wlSeatGetKeyboard {
			id := r.uint()
			mf, err := os.CreateTemp("", "keymap")
			if err != nil {
				return
			}
			_ = os.Remove(mf.Name())
			_, _ = mf.WriteString(f.keymap + "\x00")
			f.send(id, 0, int(mf.Fd()), wlUint(1), wlUint(uint32(len(f.keymap)+1)))
			_ = mf.Close()
		}
	case "ext_data_control_manager_v1":
		id := r.uint()
		f.mu.Lock()
		switch op {
		case dcManagerCreateSource:
			f.ifaces[id] = "source"
			f.sources[id] = nil
		case dcManagerGetDevice:
			f.ifaces[id] = "device"
			f.device = id
		}
		f.mu.Unlock()
		if op == dcManagerGetDevice {
			f.announce()
		}
	case "source":
		switch op {
		case dcSourceOffer:
			m := r.str()
			f.mu.Lock()
			f.sources[obj] = append(f.sources[obj], m)
			f.mu.Unlock()
		case dcSourceDestroy:
			f.mu.Lock()
			delete(f.ifaces, obj)
			if f.selSrc == obj {
				f.selSrc = 0
				f.external = nil
			}
			f.mu.Unlock()
			f.send(1, 1, -1, wlUint(obj))
		}
	case "device":
		if op == dcDeviceSetSelection {
			src := r.uint()
			f.mu.Lock()
			old := f.selSrc
			f.selSrc = src
			f.external = nil
			f.mu.Unlock()
			if old != 0 && old != src {
				f.send(old, dcSourceCancelled, -1)
			}
			f.announce()
		}
	case "offer":
		if op == dcOfferReceive {
			mime := r.str()
			wfd := fd()
			f.mu.Lock()
			src := f.offers[obj]
			ext := f.external
			f.mu.Unlock()
			if src != 0 {
				f.send(src, dcSourceSend, wfd, wlString(mime))
				_ = syscall.Close(wfd)
				return
			}
			go func() {
				w := os.NewFile(uintptr(wfd), "w")
				for _, it := range ext {
					if it.mime == mime {
						_, _ = w.Write(it.data)
					}
				}
				_ = w.Close()
			}()
		}
	}
}

// announce tells the client's device about the current selection.
func (f *fakeCompositor) announce() {
	f.mu.Lock()
	dev := f.device
	src := f.selSrc
	var mimes []string
	if src != 0 {
		mimes = append(mimes, f.sources[src]...)
	} else {
		for _, it := range f.external {
			mimes = append(mimes, it.mime)
		}
	}
	empty := src == 0 && f.external == nil
	id := uint32(0)
	if !empty {
		f.nextSrv++
		id = f.nextSrv
		f.ifaces[id] = "offer"
		f.offers[id] = src
	}
	f.mu.Unlock()
	if dev == 0 {
		return
	}
	if !empty {
		f.send(dev, dcDeviceDataOffer, -1, wlUint(id))
		for _, m := range mimes {
			f.send(id, dcOfferOffer, -1, wlString(m))
		}
	}
	f.send(dev, dcDeviceSelection, -1, wlUint(id))
}

// copyExternal makes another app own the clipboard with c (nil clears it).
func (f *fakeCompositor) copyExternal(c clip) {
	f.mu.Lock()
	old := f.selSrc
	f.selSrc = 0
	f.external = c
	f.mu.Unlock()
	if old != 0 {
		f.send(old, dcSourceCancelled, -1)
	}
	f.announce()
}

// appRead reads the clipboard as the focused app would.
func (f *fakeCompositor) appRead(mime string) (string, error) {
	f.mu.Lock()
	src := f.selSrc
	ext := f.external
	f.reads = append(f.reads, mime)
	f.mu.Unlock()
	if src == 0 {
		for _, it := range ext {
			if it.mime == mime {
				return string(it.data), nil
			}
		}
		return "", nil
	}
	var p [2]int
	if err := syscall.Pipe2(p[:], syscall.O_CLOEXEC); err != nil {
		return "", err
	}
	f.send(src, dcSourceSend, p[1], wlString(mime))
	_ = syscall.Close(p[1])
	r := os.NewFile(uintptr(p[0]), "r")
	defer func() { _ = r.Close() }()
	var b bytes.Buffer
	done := make(chan error, 1)
	go func() { _, err := io.Copy(&b, r); done <- err }()
	select {
	case err := <-done:
		return b.String(), err
	case <-time.After(3 * time.Second):
		return "", io.ErrNoProgress
	}
}

// selection describes who owns the clipboard: the client's offered MIME
// types, or the external clip.
func (f *fakeCompositor) selection() (own []string, ext clip) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selSrc != 0 {
		return append([]string(nil), f.sources[f.selSrc]...), nil
	}
	return nil, f.external
}
