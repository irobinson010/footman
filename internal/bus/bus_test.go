package bus_test

import (
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
)

var (
	numbers = bus.NewTopic[int]("test.numbers")
	words   = bus.NewTopic[string]("test.words")
)

func fixedClock() time.Time { return time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC) }

func TestPublishSubscribe(t *testing.T) {
	b := bus.New(bus.WithClock(fixedClock))
	defer b.Close()
	n := bus.Subscribe(b, numbers, 4)
	w := bus.Subscribe(b, words, 4)

	if seq := bus.Publish(b, numbers, 7); seq != 1 {
		t.Fatalf("first seq = %d, want 1", seq)
	}
	bus.Publish(b, words, "hello")

	ev := <-n.C()
	if ev.Data != 7 || ev.Topic != "test.numbers" || ev.Seq != 1 || !ev.At.Equal(fixedClock()) {
		t.Errorf("numbers event = %+v", ev)
	}
	if ev := <-w.C(); ev.Data != "hello" || ev.Seq != 2 {
		t.Errorf("words event = %+v", ev)
	}
	select {
	case ev := <-n.C():
		t.Errorf("numbers got a stray event %+v", ev)
	default:
	}
}

func TestSubscribeAll(t *testing.T) {
	b := bus.New()
	defer b.Close()
	all := bus.SubscribeAll(b, 8)
	bus.Publish(b, numbers, 1)
	bus.Publish(b, words, "two")
	got := []any{(<-all.C()).Data, (<-all.C()).Data}
	if got[0] != 1 || got[1] != "two" {
		t.Errorf("SubscribeAll got %v", got)
	}
}

func TestSlowSubscriberDropsWithoutBlocking(t *testing.T) {
	b := bus.New()
	defer b.Close()
	slow := bus.Subscribe(b, numbers, 2)
	fast := bus.Subscribe(b, numbers, 10)
	for i := range 5 {
		bus.Publish(b, numbers, i) // must not block although slow is never read
	}
	if got := slow.Dropped(); got != 3 {
		t.Errorf("slow.Dropped() = %d, want 3", got)
	}
	if got := fast.Dropped(); got != 0 {
		t.Errorf("fast.Dropped() = %d, want 0", got)
	}
	if got := b.Dropped(); got != 3 {
		t.Errorf("bus Dropped() = %d, want 3", got)
	}
	// The slow subscriber keeps the oldest events; the gap shows in Seq.
	if a, c := <-slow.C(), <-slow.C(); a.Data != 0 || c.Data != 1 {
		t.Errorf("slow kept %d, %d; want 0, 1", a.Data, c.Data)
	}
	for i := range 5 {
		if ev := <-fast.C(); ev.Data != i {
			t.Errorf("fast event %d = %d", i, ev.Data)
		}
	}
}

func TestUnsubscribe(t *testing.T) {
	b := bus.New()
	defer b.Close()
	s := bus.Subscribe(b, numbers, 4)
	bus.Publish(b, numbers, 1)
	s.Close()
	s.Close() // idempotent
	bus.Publish(b, numbers, 2)
	var got []int
	for ev := range s.C() { // buffered events stay readable, then the channel is closed
		got = append(got, ev.Data)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("after Close got %v, want [1]", got)
	}

	all := bus.SubscribeAll(b, 1)
	all.Close()
	if _, ok := <-all.C(); ok {
		t.Error("SubscribeAll channel open after Close")
	}
}

func TestCloseBus(t *testing.T) {
	b := bus.New()
	s := bus.Subscribe(b, numbers, 1)
	all := bus.SubscribeAll(b, 1)
	b.Close()
	b.Close()
	if _, ok := <-s.C(); ok {
		t.Error("subscription open after bus Close")
	}
	if _, ok := <-all.C(); ok {
		t.Error("SubscribeAll open after bus Close")
	}
	if seq := bus.Publish(b, numbers, 1); seq != 0 {
		t.Errorf("Publish on closed bus = %d, want 0", seq)
	}
	late := bus.Subscribe(b, numbers, 1)
	if _, ok := <-late.C(); ok {
		t.Error("subscription to closed bus is open")
	}
	late.Close()
	s.Close() // after bus Close: no double close
}

func TestDefaultBuffer(t *testing.T) {
	b := bus.New()
	defer b.Close()
	s := bus.Subscribe(b, numbers, 0)
	for i := range bus.DefaultBuffer + 1 {
		bus.Publish(b, numbers, i)
	}
	if s.Dropped() != 1 {
		t.Errorf("Dropped() = %d, want 1 with the default buffer", s.Dropped())
	}
}

func TestEmptyTopicPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewTopic(\"\") did not panic")
		}
	}()
	bus.NewTopic[int]("")
}

// TestConcurrent exercises publish, subscribe, unsubscribe and close together
// under -race.
func TestConcurrent(t *testing.T) {
	b := bus.New()
	var wg sync.WaitGroup
	for p := range 4 {
		wg.Go(func() {
			for i := range 200 {
				bus.Publish(b, numbers, p*1000+i)
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 50 {
				s := bus.Subscribe(b, numbers, 8)
				all := bus.SubscribeAll(b, 8)
				select {
				case <-s.C():
				default:
				}
				s.Close()
				all.Close()
			}
		})
	}
	// A reader that drains in order and checks Seq increases.
	reader := bus.Subscribe(b, numbers, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var last uint64
		for ev := range reader.C() {
			if ev.Seq <= last {
				t.Errorf("seq went from %d to %d", last, ev.Seq)
			}
			last = ev.Seq
		}
	}()
	wg.Wait()
	b.Close()
	<-done
}

func TestTopicTypeMismatchPanics(t *testing.T) {
	b := bus.New()
	defer b.Close()
	bus.Subscribe(b, numbers, 1)
	defer func() {
		if recover() == nil {
			t.Error("publishing a string on an int topic did not panic")
		}
	}()
	bus.Publish(b, bus.NewTopic[string]("test.numbers"), "seven")
}
