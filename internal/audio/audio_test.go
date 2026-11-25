package audio_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
)

func TestClipFramesAndStream(t *testing.T) {
	c := audio.Clip{Format: audio.Speech, Samples: make([]float32, 16000)}
	if c.Duration() != time.Second {
		t.Fatalf("Duration = %v", c.Duration())
	}
	frames := c.Frames(4800) // 300 ms
	if len(frames) != 4 || len(frames[3].Samples) != 1600 || frames[2].Offset != 600*time.Millisecond {
		t.Fatalf("Frames = %d frames, last %d samples", len(frames), len(frames[len(frames)-1].Samples))
	}

	got, err := audio.ReadAll(audio.NewClipStream(c, 480))
	if err != nil || len(got.Samples) != 16000 || got.Format != audio.Speech {
		t.Fatalf("ReadAll = %d samples, %v", len(got.Samples), err)
	}

	s := audio.NewClipStream(c, 480)
	_ = s.Close()
	if _, err := s.Recv(); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("Recv after Close = %v", err)
	}
}

func TestFeed(t *testing.T) {
	c := audio.Clip{Format: audio.Speech, Samples: make([]float32, 1000)}
	n := 0
	for range audio.Feed(context.Background(), c.Frames(100)) {
		n++
	}
	if n != 10 {
		t.Fatalf("fed %d frames, want 10", n)
	}
}
