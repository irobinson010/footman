package api

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	speechfake "github.com/irobinson010/footman/internal/speech/fake"
)

func speechClient(t *testing.T, stt speech.STT, vad speech.VAD) *Client {
	t.Helper()
	s := NewServer()
	Register(s, Deps{STT: stt, VAD: vad, Info: Info{Implementations: map[string]string{"stt": "fake", "vad": "fake"}}})
	a, b := net.Pipe()
	s.ServeConn(a)
	c := NewClient(b)
	t.Cleanup(func() {
		_ = c.Close()
		s.Close()
	})
	return c
}

// twoWords writes a WAV at 48 kHz stereo: silence, a burst, silence, a
// burst, silence (half a second each).
func twoWords(t *testing.T) string {
	t.Helper()
	f := audio.Format{SampleRate: 48000, Channels: 2}
	half := f.SampleRate * f.Channels / 2
	var s []float32
	for i := range 5 {
		v := float32(0)
		if i%2 == 1 {
			v = 0.5
		}
		for range half {
			s = append(s, v)
		}
	}
	p := filepath.Join(t.TempDir(), "two.wav")
	w, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := audio.EncodeWAV(w, audio.Clip{Format: f, Samples: s}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSpeechTranscribe(t *testing.T) {
	stt, _ := speechfake.NewSTT(registry.Map{"transcripts": []string{"one", "two", "whole"}})
	vad, _ := speechfake.NewVAD(registry.Map{"silence": 5})
	c := speechClient(t, stt, vad)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wav := twoWords(t)

	var r TranscribeResult
	if err := c.Call(ctx, "speech.transcribe", TranscribeParams{Path: wav}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Text != "one two" || len(r.Segments) != 2 || r.DurationMS != 2500 || r.STT != "fake" || r.VAD != "fake" {
		t.Fatalf("result %+v", r)
	}
	if s := r.Segments[0]; s.StartMS != 500 || s.EndMS != 1000 || s.Text != "one" {
		t.Errorf("segment 0: %+v", s)
	}
	if s := r.Segments[1]; s.StartMS != 1500 || s.EndMS != 2000 {
		t.Errorf("segment 1: %+v", s)
	}
	// The engines got 16 kHz mono.
	for _, in := range stt.Inputs() {
		if in.Format != audio.Speech || in.Duration() != 500*time.Millisecond {
			t.Errorf("STT got %v, %s", in.Format, in.Duration())
		}
	}

	no := false
	if err := c.Call(ctx, "speech.transcribe", TranscribeParams{Path: wav, VAD: &no}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Text != "whole" || len(r.Segments) != 1 || r.VAD != "" || r.Segments[0].EndMS != 2500 {
		t.Errorf("without VAD: %+v", r)
	}

	// The fake's queue is now empty: the engine's error is reported.
	err := c.Call(ctx, "speech.transcribe", TranscribeParams{Path: wav, VAD: &no}, &r)
	if code(err) != CodeFailed {
		t.Errorf("engine failure: %v", err)
	}
}

func TestSpeechTranscribeErrors(t *testing.T) {
	stt, _ := speechfake.NewSTT(registry.Map(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wav := twoWords(t)
	notWAV := filepath.Join(t.TempDir(), "x.wav")
	if err := os.WriteFile(notWAV, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		stt    speech.STT
		params any
		want   int
	}{
		{"relative path", stt, TranscribeParams{Path: "x.wav"}, CodeInvalidParams},
		{"missing file", stt, TranscribeParams{Path: "/nonexistent/x.wav"}, CodeInvalidParams},
		{"not a wav", stt, TranscribeParams{Path: notWAV}, CodeInvalidParams},
		{"unknown param", stt, map[string]any{"path": wav, "vda": false}, CodeInvalidParams},
		{"no VAD", stt, TranscribeParams{Path: wav}, CodeUnavailable},
		{"no STT", nil, TranscribeParams{Path: wav}, CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := speechClient(t, tc.stt, nil)
			err := c.Call(ctx, "speech.transcribe", tc.params, nil)
			if code(err) != tc.want {
				t.Errorf("err = %v, want code %d", err, tc.want)
			}
		})
	}
}
