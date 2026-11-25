// Package idle unloads speech models that have not been used for a while and
// loads them again on the next use (speech.unload_after, ADR-0028).
//
// A Holder owns one engine (speech-to-text, VAD or text-to-speech). Callers
// borrow it with Acquire for as long as they need it; once nobody holds it
// for the configured idle time, the Holder closes the engine, and the next
// Acquire builds a new one with the Holder's load function. The wrappers STT,
// VAD and TTS make a Holder look like the engine itself, so the pipeline does
// not know the difference.
//
// The package is plain Go: the engines are built by whatever load function it
// is given, so everything here is tested with fakes.
package idle

import (
	"errors"
	"io"
	"sync"
	"time"
)

// Timer is the part of *time.Timer a Holder uses.
type Timer interface{ Stop() bool }

// State is what a Holder reports for footman status.
type State struct {
	// Name is the area: "stt", "vad" or "tts".
	Name string
	// Engine is the registry name of the engine, such as "whispercpp".
	Engine string
	// Loaded is true while the model is in memory.
	Loaded bool
	// Since is when the model was last loaded or, if unloaded, unloaded.
	Since time.Time
	// Loads counts how many times the model has been loaded, the first
	// time included.
	Loads int
	// InUse is the number of callers holding it right now.
	InUse int
}

// Options configure a Holder. Only After is required.
type Options struct {
	// After returns the idle time after which the model is unloaded; zero
	// or less keeps it loaded. It is read each time the Holder goes idle and
	// again when the timer fires, so a config change takes effect without a
	// restart.
	After func() time.Duration
	// AfterFunc starts a timer (default time.AfterFunc). Tests replace it.
	AfterFunc func(d time.Duration, f func()) Timer
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// Holder keeps one engine and unloads it when idle. It is safe for
// concurrent use.
type Holder[T any] struct {
	name, engine string
	load         func() (T, error)
	o            Options

	mu       sync.Mutex
	v        T
	loaded   bool
	closed   bool
	users    int
	loads    int
	since    time.Time
	lastUsed time.Time
	timer    Timer
	gen      uint64 // bumped whenever a pending timer must be ignored
}

// New returns a Holder for engine, already loaded and idle. load builds a new
// engine after it has been unloaded.
func New[T any](name, engine string, initial T, load func() (T, error), o Options) *Holder[T] {
	if o.After == nil {
		o.After = func() time.Duration { return 0 }
	}
	if o.AfterFunc == nil {
		o.AfterFunc = func(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	h := &Holder[T]{name: name, engine: engine, load: load, o: o, v: initial, loaded: true, loads: 1}
	h.since = o.Now()
	h.lastUsed = h.since
	h.mu.Lock()
	h.armLocked()
	h.mu.Unlock()
	return h
}

// ErrClosed is returned by Acquire after Close.
var ErrClosed = errors.New("idle: closed")

// Acquire returns the engine, loading it first if it was unloaded, and holds
// it until release is called. Release exactly once; further calls do
// nothing.
func (h *Holder[T]) Acquire() (v T, release func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return v, nil, ErrClosed
	}
	h.stopTimerLocked()
	if !h.loaded {
		nv, err := h.load()
		if err != nil {
			h.armLocked()
			return v, nil, err
		}
		h.v, h.loaded = nv, true
		h.loads++
		h.since = h.o.Now()
	}
	h.users++
	var once sync.Once
	return h.v, func() { once.Do(h.release) }, nil
}

func (h *Holder[T]) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.users--
	h.lastUsed = h.o.Now()
	if h.users == 0 {
		h.armLocked()
	}
}

// Unload closes the engine now if nobody is using it, and reports whether it
// did.
func (h *Holder[T]) Unload() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unloadLocked()
}

func (h *Holder[T]) unloadLocked() bool {
	if !h.loaded || h.users > 0 {
		return false
	}
	h.stopTimerLocked()
	closeValue(h.v)
	var zero T
	h.v, h.loaded = zero, false
	h.since = h.o.Now()
	return true
}

// Close closes the engine for good. Acquire fails afterwards.
func (h *Holder[T]) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.stopTimerLocked()
	if h.loaded {
		closeValue(h.v)
		var zero T
		h.v, h.loaded = zero, false
		h.since = h.o.Now()
	}
	return nil
}

// State reports whether the model is loaded.
func (h *Holder[T]) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return State{Name: h.name, Engine: h.engine, Loaded: h.loaded, Since: h.since, Loads: h.loads, InUse: h.users}
}

func (h *Holder[T]) stopTimerLocked() {
	h.gen++
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// armLocked starts the idle timer if the engine is loaded, unused and
// unloading is on.
func (h *Holder[T]) armLocked() {
	h.stopTimerLocked()
	if !h.loaded || h.users > 0 || h.closed {
		return
	}
	d := h.o.After()
	if d <= 0 {
		return
	}
	h.armForLocked(d)
}

func (h *Holder[T]) armForLocked(d time.Duration) {
	gen := h.gen
	h.timer = h.o.AfterFunc(d, func() { h.expired(gen) })
}

func (h *Holder[T]) expired(gen uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if gen != h.gen || !h.loaded || h.users > 0 || h.closed {
		return
	}
	h.timer = nil
	d := h.o.After()
	if d <= 0 {
		return // switched off since the timer started
	}
	if idle := h.o.Now().Sub(h.lastUsed); idle < d {
		h.armForLocked(d - idle) // the setting was raised: wait the rest
		return
	}
	h.unloadLocked()
}

func closeValue(v any) {
	if c, ok := v.(io.Closer); ok {
		_ = c.Close()
	}
}
