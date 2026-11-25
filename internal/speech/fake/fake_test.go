package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/fake"
)

// frames returns 10 ms frames: loud where pattern is true, silent elsewhere.
func frames(pattern ...bool) []speech.Frame {
	var c speech.Audio
	c.Format = audio.Speech
	for _, loud := range pattern {
		v := float32(0)
		if loud {
			v = 0.5
		}
		for range 160 {
			c.Samples = append(c.Samples, v)
		}
	}
	return c.Frames(160)
}

func TestVAD(t *testing.T) {
	v, err := speech.VADs.New(fake.Name, registry.Map{"silence": 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := frames(false, true, true, false, false, true, false)
	var segs []speech.Segment
	for s := range v.Detect(ctx, audio.Feed(ctx, in)) {
		segs = append(segs, s)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if segs[0].Start != 10*time.Millisecond || segs[0].End != 30*time.Millisecond || len(segs[0].Audio.Samples) != 320 {
		t.Fatalf("first segment = %v..%v, %d samples", segs[0].Start, segs[0].End, len(segs[0].Audio.Samples))
	}
	if segs[1].Start != 50*time.Millisecond {
		t.Fatalf("second segment starts at %v", segs[1].Start)
	}
}

func TestWakeWord(t *testing.T) {
	w, err := speech.WakeWords.New(fake.Name, registry.Map{"keyword": "ashford", "at": []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	w.(*fake.WakeWord).Schedule(3)
	ctx := context.Background()
	var got []speech.WakeEvent
	for e := range w.Listen(ctx, audio.Feed(ctx, frames(false, false, false, false, false))) {
		got = append(got, e)
	}
	if len(got) != 2 || got[0].Keyword != "ashford" || got[0].At != 20*time.Millisecond || got[1].At != 40*time.Millisecond {
		t.Fatalf("events = %+v", got)
	}
}

func TestSTT(t *testing.T) {
	s, err := speech.STTs.New(fake.Name, registry.Map{"transcripts": []string{"open youtube"}})
	if err != nil {
		t.Fatal(err)
	}
	s.(*fake.STT).Queue(speech.Transcript{Text: "scratch that"})
	ctx := context.Background()
	in := speech.Audio{Format: audio.Speech, Samples: make([]float32, 100)}
	for _, want := range []string{"open youtube", "scratch that"} {
		tr, err := s.Transcribe(ctx, in)
		if err != nil || tr.Text != want {
			t.Fatalf("Transcribe = %q, %v; want %q", tr.Text, err, want)
		}
	}
	if _, err := s.Transcribe(ctx, in); !errors.Is(err, fake.ErrNoTranscript) {
		t.Fatalf("empty queue err = %v", err)
	}
	if n := len(s.(*fake.STT).Inputs()); n != 3 {
		t.Fatalf("recorded %d inputs, want 3", n)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Transcribe(cctx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx err = %v", err)
	}
}

func TestTTS(t *testing.T) {
	tts, err := speech.TTSs.New(fake.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := speech.Voice{ID: "ashford"}
	st, err := tts.Speak(context.Background(), "Very good.", v)
	if err != nil {
		t.Fatal(err)
	}
	c, err := audio.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	if c.Duration() != 100*time.Millisecond {
		t.Fatalf("audio lasts %v, want 100ms", c.Duration())
	}
	if sp := tts.(*fake.TTS).Spoken(); len(sp) != 1 || sp[0].Text != "Very good." || sp[0].Voice != v {
		t.Fatalf("Spoken = %+v", sp)
	}

	boom := errors.New("boom")
	tts.(*fake.TTS).Err = boom
	if _, err := tts.Speak(context.Background(), "x", v); !errors.Is(err, boom) {
		t.Fatalf("Err not returned: %v", err)
	}
}
