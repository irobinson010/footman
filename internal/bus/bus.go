package bus

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBuffer is the subscriber buffer used when Subscribe is given a size
// below 1.
const DefaultBuffer = 64

// Topic names a stream of events whose payload has type T. Topics are
// compared by name, so two Topic values with the same name and type are the
// same topic; using one name with two payload types panics. Declare each
// topic once, as a package-level variable, next to its payload type.
type Topic[T any] struct{ name string }

// NewTopic returns the topic called name. Names are dotted lower case, such as
// "pipeline.state"; the first part names the area that publishes it.
func NewTopic[T any](name string) Topic[T] {
	if name == "" {
		panic("bus: empty topic name")
	}
	return Topic[T]{name: name}
}

// Name returns the topic name.
func (t Topic[T]) Name() string { return t.name }

// Event is one published event.
type Event[T any] struct {
	// Topic is the name of the topic it was published on.
	Topic string
	// Seq numbers events across the whole bus, starting at 1, in publish
	// order. A subscriber that sees a gap has had events dropped (or was not
	// subscribed to the topics in between).
	Seq uint64
	// At is when it was published, by the bus clock.
	At time.Time
	// Data is the payload.
	Data T
}

// Option configures a Bus.
type Option func(*Bus)

// WithClock sets the function that stamps Event.At; tests pass a fake clock.
func WithClock(now func() time.Time) Option {
	return func(b *Bus) { b.now = now }
}

// Bus is an in-process publish/subscribe bus. The zero value is not usable;
// use New. A Bus is safe for concurrent use.
type Bus struct {
	now func() time.Time

	// mu serializes publishing, subscribing and closing, so every subscriber
	// sees events in Seq order and a channel is never closed during a send.
	// Sends are non-blocking, so holding it is cheap.
	mu      sync.Mutex
	seq     uint64
	nextID  uint64
	topics  map[string]map[uint64]*sub
	types   map[string]reflect.Type
	all     map[uint64]*sub
	closed  bool
	dropped atomic.Uint64
}

// sub is the untyped side of a subscription, held by the bus.
type sub struct {
	// deliver tries to queue an event without blocking and reports whether
	// it fit.
	deliver func(topic string, seq uint64, at time.Time, data any) bool
	// close closes the subscriber's channel. It is called with Bus.mu held,
	// exactly once.
	close   func()
	dropped atomic.Uint64
}

// New returns an empty bus.
func New(opts ...Option) *Bus {
	b := &Bus{now: time.Now, topics: map[string]map[uint64]*sub{}, types: map[string]reflect.Type{}, all: map[uint64]*sub{}}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Publish sends data on topic t to every current subscriber of t and every
// SubscribeAll subscriber. It never blocks: a subscriber whose buffer is full
// misses the event, and its drop counter (and the bus's) goes up. Publishing
// on a closed bus does nothing. It returns the event's sequence number, or 0
// if the bus is closed.
func Publish[T any](b *Bus, t Topic[T], data T) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0
	}
	checkType(b, t)
	b.seq++
	seq, at := b.seq, b.now()
	for _, s := range b.topics[t.name] {
		b.offer(s, t.name, seq, at, data)
	}
	for _, s := range b.all {
		b.offer(s, t.name, seq, at, data)
	}
	return seq
}

func (b *Bus) offer(s *sub, topic string, seq uint64, at time.Time, data any) {
	if !s.deliver(topic, seq, at, data) {
		s.dropped.Add(1)
		b.dropped.Add(1)
	}
}

// Subscription is one subscriber's view of a topic. Read events from C until
// it is closed, which happens after Close or when the bus is closed.
type Subscription[T any] struct {
	b  *Bus
	id uint64
	ch chan Event[T]
	s  *sub
	// topic is "" for SubscribeAll.
	topic string
}

// C returns the channel events arrive on.
func (s *Subscription[T]) C() <-chan Event[T] { return s.ch }

// Dropped returns how many events this subscriber has missed because its
// buffer was full.
func (s *Subscription[T]) Dropped() uint64 { return s.s.dropped.Load() }

// Close unsubscribes and closes C. Events already buffered stay readable. It
// is safe to call more than once, and after the bus is closed.
func (s *Subscription[T]) Close() {
	b := s.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.topic == "" {
		if _, ok := b.all[s.id]; !ok {
			return
		}
		delete(b.all, s.id)
	} else {
		subs := b.topics[s.topic]
		if _, ok := subs[s.id]; !ok {
			return
		}
		delete(subs, s.id)
		if len(subs) == 0 {
			delete(b.topics, s.topic)
		}
	}
	s.s.close()
}

// Subscribe subscribes to topic t with a buffer of size events (DefaultBuffer
// if size < 1). On a closed bus it returns a subscription whose channel is
// already closed.
func Subscribe[T any](b *Bus, t Topic[T], size int) *Subscription[T] {
	ch := newChan[T](size)
	s := &sub{
		deliver: func(topic string, seq uint64, at time.Time, data any) bool {
			select {
			case ch <- Event[T]{Topic: topic, Seq: seq, At: at, Data: data.(T)}:
				return true
			default:
				return false
			}
		},
		close: func() { close(ch) },
	}
	sb := &Subscription[T]{b: b, ch: ch, s: s, topic: t.name}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return sb
	}
	checkType(b, t)
	b.nextID++
	sb.id = b.nextID
	if b.topics[t.name] == nil {
		b.topics[t.name] = map[uint64]*sub{}
	}
	b.topics[t.name][sb.id] = s
	return sb
}

// SubscribeAll subscribes to every topic, with payloads untyped. It is meant
// for the local API's event stream and for logging.
func SubscribeAll(b *Bus, size int) *Subscription[any] {
	ch := newChan[any](size)
	s := &sub{
		deliver: func(topic string, seq uint64, at time.Time, data any) bool {
			select {
			case ch <- Event[any]{Topic: topic, Seq: seq, At: at, Data: data}:
				return true
			default:
				return false
			}
		},
		close: func() { close(ch) },
	}
	sb := &Subscription[any]{b: b, ch: ch, s: s}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return sb
	}
	b.nextID++
	sb.id = b.nextID
	b.all[sb.id] = s
	return sb
}

// checkType panics if t's name was first used with a different payload type,
// which would otherwise fail at delivery. b.mu must be held.
func checkType[T any](b *Bus, t Topic[T]) {
	want := reflect.TypeFor[T]()
	if got, ok := b.types[t.name]; !ok {
		b.types[t.name] = want
	} else if got != want {
		panic(fmt.Sprintf("bus: topic %q used with %v and %v", t.name, got, want))
	}
}

func newChan[T any](size int) chan Event[T] {
	if size < 1 {
		size = DefaultBuffer
	}
	return make(chan Event[T], size)
}

// Dropped returns how many deliveries have been dropped across all
// subscribers since the bus was created.
func (b *Bus) Dropped() uint64 { return b.dropped.Load() }

// Close closes every subscription and makes later Publish calls do nothing.
// It is safe to call more than once.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, subs := range b.topics {
		for _, s := range subs {
			s.close()
		}
	}
	for _, s := range b.all {
		s.close()
	}
	b.topics, b.all = map[string]map[uint64]*sub{}, map[uint64]*sub{}
}
