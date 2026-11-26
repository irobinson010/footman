package sherpa

import (
	"context"
	"io"
	"sync"

	"github.com/irobinson010/footman/internal/audio"
)

// genStream is an audio.Stream fed by a generator running in its own
// goroutine: the TTS engine's synthesis loop. The generator never waits for
// the reader (a reply's audio is small, about 3 MB a minute), so synthesis
// keeps ahead of playback. Close, or cancelling ctx, stops the generator at
// its next emit and waits for it to return, so the engine is idle
// afterwards.
type genStream struct {
	format audio.Format
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []audio.Frame
	finished bool
	err      error
	closed   bool
}

// emitFunc hands samples to the stream; it returns false when the stream
// is closed or cancelled and the generator should stop.
type emitFunc func(samples []float32) bool

// startStream runs gen in a goroutine and returns the stream of what it
// emits, cut into frames of frameSize samples. When emit returns false, gen
// should stop and return ctx.Err(); whatever gen returns is what the reader
// gets after the last frame (nil means io.EOF).
func startStream(ctx context.Context, format audio.Format, frameSize int, gen func(ctx context.Context, emit emitFunc) error) *genStream {
	ctx, cancel := context.WithCancel(ctx)
	s := &genStream{format: format, cancel: cancel, done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	frameSize = max(frameSize, 1)
	pos := 0
	emit := func(samples []float32) bool {
		if ctx.Err() != nil {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return false
		}
		for len(samples) > 0 {
			n := min(frameSize, len(samples))
			s.queue = append(s.queue, audio.Frame{Format: format, Samples: samples[:n:n], Offset: format.Duration(pos)})
			pos += n
			samples = samples[n:]
		}
		s.cond.Broadcast()
		return true
	}
	go func() {
		defer close(s.done)
		err := gen(ctx, emit)
		s.mu.Lock()
		s.finished, s.err = true, err
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	return s
}

// Format implements audio.Stream.
func (s *genStream) Format() audio.Format { return s.format }

// Recv implements audio.Stream. It waits for the next frame; after the last
// one it returns io.EOF, or the generator's error (such as
// context.Canceled).
func (s *genStream) Recv() (audio.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 && !s.finished && !s.closed {
		s.cond.Wait()
	}
	switch {
	case s.closed:
		return audio.Frame{}, audio.ErrClosed
	case len(s.queue) > 0:
		f := s.queue[0]
		s.queue[0] = audio.Frame{}
		s.queue = s.queue[1:]
		return f, nil
	case s.err != nil:
		return audio.Frame{}, s.err
	}
	return audio.Frame{}, io.EOF
}

// Close implements audio.Stream: it stops the generator and waits for it.
// It is safe to call more than once.
func (s *genStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.queue = nil
	s.cond.Broadcast()
	s.mu.Unlock()
	s.cancel()
	<-s.done
	return nil
}
