//go:build native && cgo

package sherpa

import (
	"context"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

func TestParakeet(t *testing.T) {
	dir := speechtest.ModelsDir(t, "parakeet-tdt-0.6b-v2")
	start := time.Now()
	p, err := OpenParakeet(ParakeetOptions{Common: Common{ModelsDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	t.Logf("loaded in %s: %s", time.Since(start).Round(time.Millisecond), p.Info())

	clip := speechtest.Clip(t)
	start = time.Now()
	tr, err := p.Transcribe(context.Background(), clip)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transcribed %s in %s: %q", clip.Duration(), time.Since(start).Round(time.Millisecond), tr.Text)
	if w := speechtest.WER(speechtest.Reference, tr.Text); w > 0.1 {
		t.Errorf("word error rate %.0f%%: %q", 100*w, tr.Text)
	}
	if _, err := p.Transcribe(context.Background(), speech.Audio{Format: audio.Speech}); err != nil {
		t.Errorf("empty audio: %v", err)
	}
	_ = p.Close()
	if _, err := p.Transcribe(context.Background(), clip); err == nil {
		t.Error("transcribed after Close")
	}
}

func TestParakeetRejects(t *testing.T) {
	dir := speechtest.ModelsDir(t, "parakeet-tdt-0.6b-v2")
	for _, o := range []ParakeetOptions{
		{Common: Common{ModelsDir: dir}, Accelerator: "vulkan"},
		{Common: Common{ModelsDir: dir}, Language: "es"},
		{Common: Common{ModelsDir: t.TempDir()}},
	} {
		if p, err := OpenParakeet(o); err == nil {
			_ = p.Close()
			t.Errorf("%+v accepted", o)
		}
	}
}

func TestSilero(t *testing.T) {
	dir := speechtest.ModelsDir(t, "silero-vad")
	v, err := speech.VADs.New(SileroName, registry.Map{"models_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	// One second of silence, the clip, two seconds of silence, the clip
	// again and one more second: two segments, fed in 20 ms frames that
	// start at an offset of 10 s.
	clip := speechtest.Clip(t)
	sec := audio.Speech.SampleRate
	var s []float32
	s = append(s, make([]float32, sec)...)
	s = append(s, clip.Samples...)
	s = append(s, make([]float32, 2*sec)...)
	s = append(s, clip.Samples...)
	s = append(s, make([]float32, sec)...)
	frames := audio.Clip{Format: audio.Speech, Samples: s}.Frames(320)
	for i := range frames {
		frames[i].Offset += 10 * time.Second
	}
	ctx := context.Background()
	start := time.Now()
	var segs []speech.Segment
	for seg := range v.Detect(ctx, audio.Feed(ctx, frames)) {
		segs = append(segs, seg)
	}
	t.Logf("VAD over %s took %s", audio.Speech.Duration(len(s)), time.Since(start).Round(time.Millisecond))
	if len(segs) != 2 {
		for _, g := range segs {
			t.Logf("segment %s-%s", g.Start, g.End)
		}
		t.Fatalf("%d segments, want 2", len(segs))
	}
	for i, want := range []time.Duration{11 * time.Second, 18 * time.Second} {
		g := segs[i]
		t.Logf("segment %d: %s-%s", i, g.Start, g.End)
		// The clip has about half a second of silence at each end.
		if d := g.Start - want; d < 0 || d > time.Second {
			t.Errorf("segment %d starts at %s, want within a second after %s", i, g.Start, want)
		}
		if got := g.End - g.Start; got < 3500*time.Millisecond || got > 5500*time.Millisecond {
			t.Errorf("segment %d lasts %s, want 3.5 to 5.5s", i, got)
		}
		if g.Audio.Duration() != g.End-g.Start {
			t.Errorf("segment %d holds %s of audio for %s", i, g.Audio.Duration(), g.End-g.Start)
		}
	}

	// Speech still going when the frames end is flushed as a segment.
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	var n int
	for range v.Detect(ctx2, audio.Feed(ctx2, clip.Frames(480))) {
		n++
	}
	if n != 1 {
		t.Errorf("open speech at the end: %d segments, want 1", n)
	}
}
