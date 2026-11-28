package idle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/speech"
)

// clock is a manual clock with timers.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*ctimer
}

type ctimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (t *ctimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

func newClock() *clock { return &clock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &ctimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// advance moves time forward and fires the timers that are due, in order.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*ctimer
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

type fakeSTT struct {
	id     int
	closed bool
}

func (f *fakeSTT) Transcribe(context.Context, speech.Audio) (speech.Transcript, error) {
	return speech.Transcript{Text: "hello"}, nil
}
func (f *fakeSTT) Close() error { f.closed = true; return nil }

type factory struct {
	built []*fakeSTT
	err   error
}

func (f *factory) load() (speech.STT, error) {
	if f.err != nil {
		return nil, f.err
	}
	s := &fakeSTT{id: len(f.built) + 1}
	f.built = append(f.built, s)
	return s, nil
}

func setup(t *testing.T, after time.Duration) (*Holder[speech.STT], *factory, *clock) {
	t.Helper()
	c := newClock()
	f := &factory{}
	first, _ := f.load()
	h := New[speech.STT]("stt", "fake", first, f.load, Options{
		After: func() time.Duration { return after }, AfterFunc: c.AfterFunc, Now: c.Now,
	})
	return h, f, c
}

func TestUnloadsAfterIdleAndReloadsOnUse(t *testing.T) {
	h, f, c := setup(t, 10*time.Minute)
	s := STT{H: h}

	if st := h.State(); !st.Loaded || st.Loads != 1 {
		t.Fatalf("at start: %+v", st)
	}
	c.advance(9 * time.Minute)
	if !h.State().Loaded {
		t.Fatal("unloaded early")
	}
	c.advance(2 * time.Minute)
	st := h.State()
	if st.Loaded || !f.built[0].closed {
		t.Fatalf("not unloaded after idle: %+v closed=%v", st, f.built[0].closed)
	}

	// The next use loads it again.
	tr, err := s.Transcribe(context.Background(), speech.Audio{})
	if err != nil || tr.Text != "hello" {
		t.Fatalf("Transcribe = %v, %v", tr, err)
	}
	st = h.State()
	if !st.Loaded || st.Loads != 2 || len(f.built) != 2 {
		t.Fatalf("after reload: %+v built=%d", st, len(f.built))
	}
}

func TestUseRestartsTheIdleClock(t *testing.T) {
	h, _, c := setup(t, 10*time.Minute)
	s := STT{H: h}
	c.advance(8 * time.Minute)
	if _, err := s.Transcribe(context.Background(), speech.Audio{}); err != nil {
		t.Fatal(err)
	}
	c.advance(8 * time.Minute) // 16 minutes since start, 8 since use
	if !h.State().Loaded {
		t.Fatal("unloaded though used 8 minutes ago")
	}
	c.advance(3 * time.Minute)
	if h.State().Loaded {
		t.Fatal("still loaded 11 minutes after the last use")
	}
}

func TestNeverUnloadsWhileHeld(t *testing.T) {
	h, _, c := setup(t, time.Minute)
	_, release, err := h.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	c.advance(time.Hour)
	if st := h.State(); !st.Loaded || st.InUse != 1 {
		t.Fatalf("held engine was unloaded: %+v", st)
	}
	if h.Unload() {
		t.Fatal("Unload closed an engine in use")
	}
	release()
	release() // a second release is ignored
	if st := h.State(); st.InUse != 0 {
		t.Fatalf("InUse = %d", st.InUse)
	}
	c.advance(2 * time.Minute)
	if h.State().Loaded {
		t.Fatal("not unloaded after release and idle time")
	}
}

func TestZeroKeepsLoaded(t *testing.T) {
	h, _, c := setup(t, 0)
	c.advance(24 * time.Hour)
	if !h.State().Loaded {
		t.Fatal("unloaded with unload_after = 0")
	}
}

func TestSettingChangesApplyWhenTheTimerFires(t *testing.T) {
	c := newClock()
	f := &factory{}
	first, _ := f.load()
	after := 10 * time.Minute
	h := New[speech.STT]("stt", "fake", first, f.load, Options{
		After: func() time.Duration { return after }, AfterFunc: c.AfterFunc, Now: c.Now,
	})
	after = 30 * time.Minute // raised while the 10-minute timer runs
	c.advance(11 * time.Minute)
	if !h.State().Loaded {
		t.Fatal("unloaded after 11 min though the setting is now 30")
	}
	c.advance(20 * time.Minute)
	if h.State().Loaded {
		t.Fatal("still loaded 31 minutes in")
	}

	// Switched off while a timer runs.
	if _, release, err := h.Acquire(); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	after = 0
	c.advance(time.Hour)
	if !h.State().Loaded {
		t.Fatal("unloaded though unload_after was set to 0")
	}
}

func TestLoadFailureIsReportedAndRetried(t *testing.T) {
	h, f, _ := setup(t, time.Minute)
	h.Unload()
	f.err = errors.New("model file is gone")
	if _, err := (STT{H: h}).Transcribe(context.Background(), speech.Audio{}); err == nil || err.Error() != "model file is gone" {
		t.Fatalf("err = %v", err)
	}
	if h.State().Loaded {
		t.Fatal("loaded after a failed load")
	}
	f.err = nil
	if _, err := (STT{H: h}).Transcribe(context.Background(), speech.Audio{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestCloseIsFinal(t *testing.T) {
	h, f, _ := setup(t, time.Minute)
	if err := (STT{H: h}).Close(); err != nil {
		t.Fatal(err)
	}
	if !f.built[0].closed {
		t.Fatal("engine not closed")
	}
	if _, err := (STT{H: h}).Transcribe(context.Background(), speech.Audio{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// vad emits one segment per frame it reads.
type vad struct{}

func (vad) Detect(ctx context.Context, frames <-chan speech.Frame) <-chan speech.Segment {
	out := make(chan speech.Segment)
	go func() {
		defer close(out)
		for f := range frames {
			select {
			case out <- speech.Segment{End: f.Format.Duration(len(f.Samples))}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func TestVADStaysLoadedWhileListening(t *testing.T) {
	c := newClock()
	loads := 0
	h := New[speech.VAD]("vad", "fake", vad{}, func() (speech.VAD, error) { loads++; return vad{}, nil },
		Options{After: func() time.Duration { return time.Minute }, AfterFunc: c.AfterFunc, Now: c.Now})
	frames := make(chan speech.Frame)
	out := (VAD{H: h}).Detect(context.Background(), frames)
	c.advance(time.Hour)
	if !h.State().Loaded {
		t.Fatal("VAD unloaded under an open Detect")
	}
	frames <- speech.Frame{Format: audio.Speech, Samples: make([]float32, 160)}
	if seg := <-out; seg.End == 0 {
		t.Fatalf("segment = %+v", seg)
	}
	close(frames)
	for range out {
	}
	// The release happens as the stream ends.
	deadline := time.Now().Add(time.Second)
	for h.State().InUse != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	c.advance(2 * time.Minute)
	if h.State().Loaded {
		t.Fatal("VAD still loaded after listening stopped and idle time passed")
	}
	// Listening again loads it again.
	frames2 := make(chan speech.Frame)
	out2 := (VAD{H: h}).Detect(context.Background(), frames2)
	close(frames2)
	for range out2 {
	}
	if loads != 1 {
		t.Fatalf("reloaded %d times, want 1", loads)
	}
}

type stream struct{ closed int }

func (*stream) Format() audio.Format       { return audio.Speech }
func (*stream) Recv() (audio.Frame, error) { return audio.Frame{}, errors.New("eof") }
func (s *stream) Close() error             { s.closed++; return nil }

type tts struct{ s *stream }

func (t tts) Speak(context.Context, string, speech.Voice) (speech.AudioStream, error) {
	return t.s, nil
}

func TestTTSHeldUntilStreamClosed(t *testing.T) {
	c := newClock()
	st := &stream{}
	h := New[speech.TTS]("tts", "fake", tts{st}, func() (speech.TTS, error) { return tts{st}, nil },
		Options{After: func() time.Duration { return time.Minute }, AfterFunc: c.AfterFunc, Now: c.Now})
	s, err := (TTS{H: h}).Speak(context.Background(), "hi", speech.Voice{})
	if err != nil {
		t.Fatal(err)
	}
	c.advance(time.Hour)
	if !h.State().Loaded {
		t.Fatal("unloaded while speaking")
	}
	_ = s.Close()
	_ = s.Close()
	if st.closed != 2 || h.State().InUse != 0 {
		t.Fatalf("closed=%d inuse=%d", st.closed, h.State().InUse)
	}
	c.advance(2 * time.Minute)
	if h.State().Loaded {
		t.Fatal("not unloaded after the stream closed")
	}
}

func TestStateEngineFollowsTheEngineOption(t *testing.T) {
	c := newClock()
	f := &factory{}
	first, _ := f.load()
	engine := "whispercpp"
	h := New[speech.STT]("stt", engine, first, f.load, Options{
		After: func() time.Duration { return time.Minute }, AfterFunc: c.AfterFunc, Now: c.Now,
		Engine: func() string { return engine },
	})
	if got := h.State().Engine; got != "whispercpp" {
		t.Errorf("engine = %q", got)
	}
	engine = "sherpa-parakeet"
	if got := h.State().Engine; got != "sherpa-parakeet" {
		t.Errorf("engine after a reload fell back = %q", got)
	}
}
