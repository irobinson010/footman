//go:build linux

package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Data-control (ext_data_control_v1, or the older zwlr_data_control_v1 with
// the same wire layout) lets a client without a window read and set the
// clipboard. Footman uses it to paste: save the current clipboard, offer the
// dictated text, watch for the focused app reading it, then put the saved
// clipboard back.

// Opcodes, shared by both protocol versions.
const (
	dcManagerCreateSource = 0
	dcManagerGetDevice    = 1

	dcDeviceSetSelection = 0
	dcDeviceDataOffer    = 0 // event
	dcDeviceSelection    = 1 // event
	dcDeviceFinished     = 2 // event

	dcSourceOffer     = 0
	dcSourceDestroy   = 1
	dcSourceSend      = 0 // event
	dcSourceCancelled = 1 // event

	dcOfferReceive = 0
	dcOfferDestroy = 1
	dcOfferOffer   = 0 // event

	wlSeatGetKeyboard = 1
)

var dataControlManagers = []string{"ext_data_control_manager_v1", "zwlr_data_control_manager_v1"}

// Limits on saving the previous clipboard: all of it is read within
// clipReadTimeout, and at most clipMaxBytes of it is kept.
const (
	clipReadTimeout = time.Second
	clipMaxBytes    = 64 << 20
)

// passwordHint is offered with dictated text so clipboard managers such as
// Klipper do not keep it in their history.
const passwordHint = "x-kde-passwordManagerHint"

// textMimes are offered for pasted text, for native and Xwayland apps.
var textMimes = []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "STRING", "TEXT"}

// clip is a clipboard's contents: each MIME type with its data, in the
// order offered. A nil clip means "no selection".
type clip []clipItem

type clipItem struct {
	mime string
	data []byte
}

func textClip(text string) clip {
	c := make(clip, 0, len(textMimes)+1)
	for _, m := range textMimes {
		c = append(c, clipItem{m, []byte(text)})
	}
	return append(c, clipItem{passwordHint, []byte("secret")})
}

// isTextMime reports whether a read of mime means an app is reading the text,
// as opposed to a clipboard manager checking the password hint.
func isTextMime(mime string) bool {
	return mime != passwordHint
}

// dataControl owns one data-control device on the seat.
type dataControl struct {
	wl     *wlConn
	mgr    uint32
	device uint32

	mu        sync.Mutex
	offers    map[uint32][]string // offer id -> MIME types
	selection uint32              // current selection offer, 0 for none
	own       *dcSource           // our source, while it owns the selection
	finished  bool
}

// dcSource is a clipboard source this process serves.
type dcSource struct {
	dc        *dataControl
	id        uint32
	data      clip
	onRead    func(mime string) // called after each read is fully written
	mu        sync.Mutex
	cancelled bool
	gone      chan struct{} // closed when cancelled
}

// newDataControl binds the data-control manager and a device for the seat.
func newDataControl(ctx context.Context, w *wlConn) (*dataControl, error) {
	var mg wlGlobal
	found := false
	for _, name := range dataControlManagers {
		if mg, found = w.global(name); found {
			break
		}
	}
	if !found {
		return nil, errors.New("the compositor offers no data-control protocol")
	}
	seat, ok := w.global("wl_seat")
	if !ok {
		return nil, errors.New("the compositor has no seat")
	}
	dc := &dataControl{wl: w, offers: map[uint32][]string{}}
	var err error
	if dc.mgr, _, err = w.bind(mg, 1); err != nil {
		return nil, err
	}
	seatID, _, err := w.bind(seat, 1)
	if err != nil {
		return nil, err
	}
	dc.device = w.newID()
	w.handle(dc.device, dc.deviceEvent)
	if err := w.send(dc.mgr, dcManagerGetDevice, nil, wlUint(dc.device), wlUint(seatID)); err != nil {
		return nil, err
	}
	// The device announces the current selection straight away.
	if err := w.roundtrip(ctx); err != nil {
		return nil, err
	}
	return dc, nil
}

func (dc *dataControl) deviceEvent(op uint16, body []byte, _ func() int) {
	r := wlReader{b: body}
	switch op {
	case dcDeviceDataOffer:
		id := r.uint()
		dc.mu.Lock()
		dc.offers[id] = nil
		dc.mu.Unlock()
		dc.wl.handle(id, func(op uint16, body []byte, _ func() int) {
			if op != dcOfferOffer {
				return
			}
			r := wlReader{b: body}
			mime := r.str()
			dc.mu.Lock()
			if _, ok := dc.offers[id]; ok {
				dc.offers[id] = append(dc.offers[id], mime)
			}
			dc.mu.Unlock()
		})
	case dcDeviceSelection:
		id := r.uint()
		dc.mu.Lock()
		old := dc.selection
		dc.selection = id
		dc.mu.Unlock()
		if old != 0 && old != id {
			dc.dropOffer(old)
		}
	case dcDeviceFinished:
		dc.mu.Lock()
		dc.finished = true
		dc.mu.Unlock()
	}
}

func (dc *dataControl) dropOffer(id uint32) {
	dc.mu.Lock()
	delete(dc.offers, id)
	dc.mu.Unlock()
	dc.wl.handle(id, nil)
	_ = dc.wl.send(id, dcOfferDestroy, nil)
}

// usable reports whether the device still works.
func (dc *dataControl) usable() bool {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return !dc.finished && dc.wl.failed() == nil
}

// snapshot returns the current clipboard: every MIME type with its data. It
// returns nil when the clipboard is empty. A type that cannot be read in
// time is left out.
func (dc *dataControl) snapshot(ctx context.Context) (clip, error) {
	// Handle pending events first, so the selection is current.
	if err := dc.wl.roundtrip(ctx); err != nil {
		return nil, err
	}
	dc.mu.Lock()
	own := dc.own
	offer := dc.selection
	mimes := append([]string(nil), dc.offers[offer]...)
	dc.mu.Unlock()
	if own != nil && !own.isCancelled() {
		return own.data, nil
	}
	if offer == 0 {
		return nil, nil
	}
	// Read every type at once, so a slow or stuck owner costs one timeout
	// in total rather than one per type.
	deadline := time.Now().Add(clipReadTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	results := make([][]byte, len(mimes))
	var wg sync.WaitGroup
	for i, m := range mimes {
		wg.Go(func() {
			if data, err := dc.receive(offer, m, deadline); err == nil {
				results[i] = data
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out clip
	total := 0
	for i, m := range mimes {
		if results[i] == nil || total+len(results[i]) > clipMaxBytes {
			continue
		}
		total += len(results[i])
		out = append(out, clipItem{m, results[i]})
	}
	if len(out) == 0 && len(mimes) > 0 {
		return nil, errors.New("could not read the current clipboard")
	}
	return out, nil
}

// receive reads one MIME type of an offer through a pipe, until deadline.
// It returns nil data only with an error.
func (dc *dataControl) receive(offer uint32, mime string, deadline time.Time) ([]byte, error) {
	var p [2]int
	if err := syscall.Pipe2(p[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		return nil, err
	}
	r := os.NewFile(uintptr(p[0]), "clipboard-read")
	defer func() { _ = r.Close() }()
	wfd := p[1]
	err := dc.wl.send(offer, dcOfferReceive, &wfd, wlString(mime))
	_ = syscall.Close(wfd) // the compositor has its own copy now
	if err != nil {
		return nil, err
	}
	_ = r.SetReadDeadline(deadline)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(r, clipMaxBytes+1)); err != nil {
		return nil, fmt.Errorf("reading %s: %w", mime, err)
	}
	if buf.Len() > clipMaxBytes {
		return nil, fmt.Errorf("reading %s: clipboard too large", mime)
	}
	return append([]byte{}, buf.Bytes()...), nil
}

// set makes c the clipboard (nil clears it) and returns the source serving
// it. onRead, if set, is called after each completed read.
func (dc *dataControl) set(ctx context.Context, c clip, onRead func(mime string)) (*dcSource, error) {
	if c == nil {
		if err := dc.wl.send(dc.device, dcDeviceSetSelection, nil, wlUint(0)); err != nil {
			return nil, err
		}
		dc.mu.Lock()
		own := dc.own
		dc.own = nil
		dc.mu.Unlock()
		if own != nil {
			own.cancel()
		}
		return nil, dc.wl.roundtrip(ctx)
	}
	s := &dcSource{dc: dc, id: dc.wl.newID(), data: c, onRead: onRead, gone: make(chan struct{})}
	dc.wl.handle(s.id, s.event)
	if err := dc.wl.send(dc.mgr, dcManagerCreateSource, nil, wlUint(s.id)); err != nil {
		return nil, err
	}
	for _, it := range c {
		if err := dc.wl.send(s.id, dcSourceOffer, nil, wlString(it.mime)); err != nil {
			return nil, err
		}
	}
	if err := dc.wl.send(dc.device, dcDeviceSetSelection, nil, wlUint(s.id)); err != nil {
		return nil, err
	}
	dc.mu.Lock()
	prev := dc.own
	dc.own = s
	dc.mu.Unlock()
	if prev != nil {
		prev.cancel()
	}
	// Make sure the compositor has the new selection before any key press
	// that pastes it.
	return s, dc.wl.roundtrip(ctx)
}

func (s *dcSource) event(op uint16, body []byte, fd func() int) {
	switch op {
	case dcSourceSend:
		r := wlReader{b: body}
		mime := r.str()
		f := fd()
		if f < 0 {
			return
		}
		var data []byte
		for _, it := range s.data {
			if it.mime == mime {
				data = it.data
				break
			}
		}
		// Write off the event goroutine: the reader may be slow, or be us.
		go func() {
			_ = syscall.SetNonblock(f, true) // so the write deadline works
			file := os.NewFile(uintptr(f), "clipboard-send")
			_ = file.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := file.Write(data)
			_ = file.Close()
			if err == nil && s.onRead != nil {
				s.onRead(mime)
			}
		}()
	case dcSourceCancelled:
		s.cancel()
	}
}

// cancel marks the source as no longer owning the clipboard and destroys it.
func (s *dcSource) cancel() {
	s.mu.Lock()
	if s.cancelled {
		s.mu.Unlock()
		return
	}
	s.cancelled = true
	close(s.gone)
	s.mu.Unlock()
	s.dc.mu.Lock()
	if s.dc.own == s {
		s.dc.own = nil
	}
	s.dc.mu.Unlock()
	s.dc.wl.destroyed(s.id, dcSourceSend)
	_ = s.dc.wl.send(s.id, dcSourceDestroy, nil)
}

func (s *dcSource) isCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

// String describes a clip for errors and tests.
func (c clip) String() string {
	var b strings.Builder
	for i, it := range c {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s(%d)", it.mime, len(it.data))
	}
	return b.String()
}
