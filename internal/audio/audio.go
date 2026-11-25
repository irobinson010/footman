// Package audio holds the PCM primitives shared by the platform layer (which
// captures and plays audio) and the speech engines (which consume and produce
// it): Format, Frame, Clip and Stream.
//
// Samples are mono or interleaved float32 in [-1, 1], the form sherpa-onnx and
// whisper.cpp take directly. Speech is 16 kHz mono.
package audio

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Format describes PCM audio.
type Format struct {
	// SampleRate is in samples per second per channel.
	SampleRate int
	// Channels is the number of interleaved channels.
	Channels int
}

// Speech is the format the speech engines expect: 16 kHz mono.
var Speech = Format{SampleRate: 16000, Channels: 1}

// Duration returns how long n interleaved samples last in this format. It
// returns 0 for an invalid format.
func (f Format) Duration(n int) time.Duration {
	if f.SampleRate <= 0 || f.Channels <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second / time.Duration(f.SampleRate*f.Channels)
}

// Valid reports whether the format has a positive rate and channel count.
func (f Format) Valid() bool { return f.SampleRate > 0 && f.Channels > 0 }

// Frame is a short chunk of audio as it arrives from a microphone or leaves a
// synthesizer, typically 10 to 30 ms.
type Frame struct {
	// Format of Samples.
	Format Format
	// Samples are interleaved float32 PCM in [-1, 1].
	Samples []float32
	// Offset is the time of the first sample since the start of the stream.
	Offset time.Duration
}

// Duration returns how long the frame lasts.
func (f Frame) Duration() time.Duration { return f.Format.Duration(len(f.Samples)) }

// Clip is a complete piece of audio held in memory, such as one utterance.
type Clip struct {
	// Format of Samples.
	Format Format
	// Samples are interleaved float32 PCM in [-1, 1].
	Samples []float32
}

// Duration returns how long the clip lasts.
func (c Clip) Duration() time.Duration { return c.Format.Duration(len(c.Samples)) }

// Frames splits the clip into frames of n samples each (the last may be
// shorter), with offsets measured from the start of the clip. It returns nil
// if n is not positive.
func (c Clip) Frames(n int) []Frame {
	if n <= 0 {
		return nil
	}
	var out []Frame
	for i := 0; i < len(c.Samples); i += n {
		j := min(i+n, len(c.Samples))
		out = append(out, Frame{Format: c.Format, Samples: c.Samples[i:j], Offset: c.Format.Duration(i)})
	}
	return out
}

// Stream is audio delivered incrementally, such as speech being synthesized
// sentence by sentence. It is pull-based: the consumer calls Recv until it
// returns io.EOF, and calls Close when done (including early, to stop the
// producer). A Stream is not safe for concurrent use.
type Stream interface {
	// Format returns the format of every frame in the stream.
	Format() Format
	// Recv blocks until the next frame is ready. It returns io.EOF after the
	// last frame, or another error if the stream failed.
	Recv() (Frame, error)
	// Close releases the stream. It is safe to call more than once.
	Close() error
}

// ErrClosed is returned by Recv on a stream that has been closed.
var ErrClosed = errors.New("audio: stream closed")

// NewClipStream returns a Stream that yields clip in frames of n samples.
func NewClipStream(clip Clip, n int) Stream {
	return &sliceStream{format: clip.Format, frames: clip.Frames(n)}
}

type sliceStream struct {
	mu     sync.Mutex
	format Format
	frames []Frame
	closed bool
}

// Format implements Stream.
func (s *sliceStream) Format() Format { return s.format }

// Recv implements Stream.
func (s *sliceStream) Recv() (Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Frame{}, ErrClosed
	}
	if len(s.frames) == 0 {
		return Frame{}, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

// Close implements Stream.
func (s *sliceStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.frames = nil
	s.mu.Unlock()
	return nil
}

// ReadAll drains s into a single clip and closes it.
func ReadAll(s Stream) (Clip, error) {
	defer func() { _ = s.Close() }()
	c := Clip{Format: s.Format()}
	for {
		f, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return c, nil
		}
		if err != nil {
			return c, err
		}
		c.Samples = append(c.Samples, f.Samples...)
	}
}

// Feed sends frames on a new channel from a goroutine, closing the channel
// after the last frame or when ctx is done. It is a convenience for driving
// VAD and wake-word engines from a clip in tests and tools.
func Feed(ctx context.Context, frames []Frame) <-chan Frame {
	ch := make(chan Frame)
	go func() {
		defer close(ch)
		for _, f := range frames {
			select {
			case ch <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}
