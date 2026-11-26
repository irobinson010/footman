package sherpa

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
)

var testFormat = audio.Format{SampleRate: 24000, Channels: 1}

func TestStreamFrames(t *testing.T) {
	s := startStream(context.Background(), testFormat, 480, func(ctx context.Context, emit emitFunc) error {
		emit(make([]float32, 1000))
		emit(make([]float32, 200))
		return nil
	})
	if s.Format() != testFormat {
		t.Errorf("format %+v", s.Format())
	}
	var sizes []int
	var offsets []time.Duration
	for {
		f, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(f.Samples))
		offsets = append(offsets, f.Offset)
	}
	if len(sizes) != 4 || sizes[0] != 480 || sizes[2] != 40 || sizes[3] != 200 {
		t.Errorf("frame sizes %v", sizes)
	}
	if offsets[1] != 20*time.Millisecond || offsets[3] != testFormat.Duration(1000) {
		t.Errorf("offsets %v", offsets)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); !errors.Is(err, audio.ErrClosed) {
		t.Errorf("Recv after Close: %v", err)
	}
	_ = s.Close() // twice is fine
}

func TestStreamError(t *testing.T) {
	boom := errors.New("boom")
	s := startStream(context.Background(), testFormat, 480, func(ctx context.Context, emit emitFunc) error {
		emit(make([]float32, 10))
		return boom
	})
	defer func() { _ = s.Close() }()
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); !errors.Is(err, boom) {
		t.Errorf("got %v", err)
	}
}

// generator emits a chunk each time it is told to, like the engine emitting
// a sentence at a time, and reports how it ended.
func generator(next <-chan struct{}, ended chan<- error) func(ctx context.Context, emit emitFunc) error {
	return func(ctx context.Context, emit emitFunc) error {
		for {
			select {
			case <-next:
			case <-ctx.Done():
			}
			if !emit(make([]float32, 480)) {
				ended <- ctx.Err()
				return ctx.Err()
			}
		}
	}
}

func TestStreamCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	next, ended := make(chan struct{}), make(chan error, 1)
	s := startStream(ctx, testFormat, 480, generator(next, ended))
	defer func() { _ = s.Close() }()
	next <- struct{}{}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Errorf("generator ended with %v", err)
	}
	if _, err := s.Recv(); !errors.Is(err, context.Canceled) {
		t.Errorf("Recv after cancel: %v", err)
	}
}

func TestStreamCloseStopsGenerator(t *testing.T) {
	next, ended := make(chan struct{}), make(chan error, 1)
	s := startStream(context.Background(), testFormat, 480, generator(next, ended))
	next <- struct{}{}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	select {
	case <-ended:
	default:
		t.Error("Close returned before the generator stopped")
	}
}

// A reader that stops reading must not block the generator: synthesis keeps
// ahead of playback.
func TestStreamNeverBlocksGenerator(t *testing.T) {
	s := startStream(context.Background(), testFormat, 10, func(ctx context.Context, emit emitFunc) error {
		for range 1000 {
			if !emit(make([]float32, 100)) {
				return ctx.Err()
			}
		}
		return nil
	})
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("generator blocked on an idle reader")
	}
	c, err := audio.ReadAll(s)
	if err != nil || len(c.Samples) != 100000 {
		t.Errorf("read %d samples, %v", len(c.Samples), err)
	}
}
