//go:build linux

package linux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// wlConn is a small pure-Go Wayland client: enough to bind globals, send
// requests (with file descriptors) and dispatch events to per-object
// handlers. It is used for the data-control clipboard and the keyboard
// keymap, which need no surface. Events are read on one goroutine and handled
// there, so handlers must not block.
type wlConn struct {
	c *net.UnixConn

	wmu sync.Mutex // serialises writes

	mu       sync.Mutex
	nextID   uint32
	handlers map[uint32]wlHandler
	globals  []wlGlobal
	regID    uint32
	err      error
	done     chan struct{}
}

// wlHandler handles one event for an object. fd returns the next file
// descriptor that arrived with the event (for events with an fd argument).
type wlHandler func(op uint16, body []byte, fd func() int)

type wlGlobal struct {
	name    uint32
	iface   string
	version uint32
}

// errWaylandGone is returned once the compositor connection has closed.
var errWaylandGone = errors.New("wayland connection closed")

// waylandSocket returns the compositor socket path from WAYLAND_DISPLAY and
// XDG_RUNTIME_DIR, or "" if there is none.
func waylandSocket() string {
	d := os.Getenv("WAYLAND_DISPLAY")
	if d == "" || realDisplayRefused() {
		return ""
	}
	if filepath.IsAbs(d) {
		return d
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		return ""
	}
	return filepath.Join(rt, d)
}

// dialWayland connects to the compositor socket and reads its globals.
func dialWayland(ctx context.Context, socket string) (*wlConn, error) {
	if socket == "" {
		return nil, errors.New("no Wayland session (WAYLAND_DISPLAY is not set)")
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("wayland: %w", err)
	}
	w := &wlConn{c: nc.(*net.UnixConn), nextID: 2, handlers: map[uint32]wlHandler{}, done: make(chan struct{})}
	w.regID = w.newID()
	w.handle(w.regID, func(op uint16, body []byte, _ func() int) {
		if op != 0 { // wl_registry.global_remove is not needed
			return
		}
		r := wlReader{b: body}
		g := wlGlobal{name: r.uint(), iface: r.str(), version: r.uint()}
		w.mu.Lock()
		w.globals = append(w.globals, g)
		w.mu.Unlock()
	})
	go w.readLoop()
	if err := w.send(1, 1, nil, wlUint(w.regID)); err != nil { // wl_display.get_registry
		w.close()
		return nil, err
	}
	if err := w.roundtrip(ctx); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *wlConn) newID() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.nextID
	w.nextID++
	return id
}

func (w *wlConn) handle(id uint32, h wlHandler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h == nil {
		delete(w.handlers, id)
		return
	}
	w.handlers[id] = h
}

// destroyed replaces the handler of a client-created object the client has
// just destroyed. Events already in flight are dropped, but file descriptors
// they carry (events with an op in fdOps) are taken and closed so later
// events get theirs. The compositor's delete_id removes the handler.
func (w *wlConn) destroyed(id uint32, fdOps ...uint16) {
	w.handle(id, func(op uint16, _ []byte, fd func() int) {
		for _, o := range fdOps {
			if o == op {
				if f := fd(); f >= 0 {
					_ = syscall.Close(f)
				}
			}
		}
	})
}

// global returns the first advertised global with the given interface.
func (w *wlConn) global(iface string) (wlGlobal, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, g := range w.globals {
		if g.iface == iface {
			return g, true
		}
	}
	return wlGlobal{}, false
}

// bind binds g at version v (capped at what the compositor offers) and
// returns the new object's id and version.
func (w *wlConn) bind(g wlGlobal, v uint32) (uint32, uint32, error) {
	v = min(v, g.version)
	id := w.newID()
	return id, v, w.send(w.regID, 0, nil, wlUint(g.name), wlString(g.iface), wlUint(v), wlUint(id))
}

// send writes one request. fd, if not nil, is passed alongside it.
func (w *wlConn) send(obj uint32, op uint16, fd *int, args ...[]byte) error {
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
	if fd != nil {
		oob = syscall.UnixRights(*fd)
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if err := w.failed(); err != nil {
		return err
	}
	if _, _, err := w.c.WriteMsgUnix(msg, oob, nil); err != nil {
		return fmt.Errorf("wayland: %w", err)
	}
	return nil
}

// roundtrip waits until the compositor has handled every request sent so far.
func (w *wlConn) roundtrip(ctx context.Context) error {
	cb := w.newID()
	done := make(chan struct{})
	w.handle(cb, func(uint16, []byte, func() int) {
		w.handle(cb, nil)
		close(done)
	})
	if err := w.send(1, 0, nil, wlUint(cb)); err != nil { // wl_display.sync
		return err
	}
	select {
	case <-done:
		return nil
	case <-w.done:
		return w.failed()
	case <-ctx.Done():
		w.handle(cb, nil)
		return ctx.Err()
	}
}

func (w *wlConn) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *wlConn) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
		close(w.done)
	}
	w.mu.Unlock()
}

func (w *wlConn) close() {
	w.fail(errWaylandGone)
	_ = w.c.Close()
}

func (w *wlConn) readLoop() {
	defer func() { _ = w.c.Close() }()
	buf := make([]byte, 0, 8192)
	chunk := make([]byte, 8192)
	oob := make([]byte, syscall.CmsgSpace(28*4))
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
	}()
	for {
		n, oobn, _, _, err := w.c.ReadMsgUnix(chunk, oob)
		if oobn > 0 {
			if msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn]); perr == nil {
				for _, m := range msgs {
					if got, perr := syscall.ParseUnixRights(&m); perr == nil {
						fds = append(fds, got...)
					}
				}
			}
		}
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		for len(buf) >= 8 {
			obj := binary.LittleEndian.Uint32(buf)
			so := binary.LittleEndian.Uint32(buf[4:])
			size, op := int(so>>16), uint16(so&0xffff)
			if size < 8 {
				w.fail(errors.New("wayland: malformed message"))
				return
			}
			if len(buf) < size {
				break
			}
			body := append([]byte(nil), buf[8:size]...)
			buf = buf[size:]
			if obj == 1 && op == 0 { // wl_display.error
				r := wlReader{b: body}
				id, code, msg := r.uint(), r.uint(), r.str()
				w.fail(fmt.Errorf("wayland protocol error on object %d (code %d): %s", id, code, msg))
				return
			}
			if obj == 1 && op == 1 { // wl_display.delete_id: no more events for it
				r := wlReader{b: body}
				w.handle(r.uint(), nil)
				continue
			}
			w.mu.Lock()
			h := w.handlers[obj]
			w.mu.Unlock()
			takeFD := func() int {
				if len(fds) == 0 {
					return -1
				}
				fd := fds[0]
				fds = fds[1:]
				return fd
			}
			if h != nil {
				h(op, body, takeFD)
			}
		}
		if err != nil {
			w.fail(fmt.Errorf("%w: %w", errWaylandGone, err))
			return
		}
	}
}

// Wire encoding.

func wlUint(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func wlString(s string) []byte {
	n := len(s) + 1
	b := make([]byte, 4+n+(4-n%4)%4)
	binary.LittleEndian.PutUint32(b, uint32(n))
	copy(b[4:], s)
	return b
}

// wlReader decodes event arguments. Out-of-range reads return zero values.
type wlReader struct {
	b   []byte
	off int
}

func (r *wlReader) uint() uint32 {
	if r.off+4 > len(r.b) {
		r.off = len(r.b)
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *wlReader) str() string {
	n := int(r.uint())
	if n == 0 || r.off+n > len(r.b) {
		r.off = len(r.b)
		return ""
	}
	s := string(r.b[r.off : r.off+n-1])
	r.off += n + (4-n%4)%4
	return s
}
