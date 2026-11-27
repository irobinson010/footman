package clipbank_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/internal/persona/clipbank"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	sfake "github.com/irobinson010/footman/internal/speech/fake"
)

const manifest = `
[persona]
id = "tester"
name = "Tester"
version = "1.0.0"
license = "CC0-1.0"
[wake]
words = ["tester"]
[address]
title = "Master"
fallback = "sir"
[voice]
engine = "kokoro"
model = "kokoro-en"
speaker = "bm_george"
[style]
prompt = "style.md"
`

const lines = `
ack = ["Right away, {address}.", "On it.", "Very good."]
working = ["Working on it."]
done = ["It is done, {address}."]
quips = ["A quip, {address}."]
`

func pack(t *testing.T, extra fstest.MapFS) *persona.Pack {
	t.Helper()
	m := fstest.MapFS{
		"persona.toml":  {Data: []byte(manifest)},
		"style.md":      {Data: []byte("Be terse.")},
		"lines.en.toml": {Data: []byte(lines)},
	}
	for k, v := range extra {
		m[k] = v
	}
	p, err := persona.Load(m, "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func tts(t *testing.T) *sfake.TTS {
	t.Helper()
	f, err := sfake.NewTTS(registry.Map(nil))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func target(p *persona.Pack, address string) clipbank.Target {
	return clipbank.Target{Pack: p, Voice: speech.Voice{ID: "kokoro-en", Speaker: "bm_george", Speed: 1}, Address: address}
}

func wait(t *testing.T, b *clipbank.Bank) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func spoken(f *sfake.TTS) []string {
	var out []string
	for _, u := range f.Spoken() {
		out = append(out, u.Text)
	}
	return out
}

func wavs(t *testing.T, dir string) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "tester", "*.wav"))
	return len(m)
}

func TestRenderAndCache(t *testing.T) {
	dir := t.TempDir()
	f := tts(t)
	p := pack(t, nil)

	b := clipbank.New(clipbank.Options{Dir: dir, TTS: f})
	b.Set(target(p, "Master Alex"))
	wait(t, b)
	if err := b.Render(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 3 ack + 1 working + 1 done; quips are not banked.
	if n := len(f.Spoken()); n != 5 {
		t.Fatalf("rendered %d lines %v, want 5", n, spoken(f))
	}
	if n := wavs(t, dir); n != 5 {
		t.Errorf("%d cached files, want 5", n)
	}
	voice := f.Spoken()[0].Voice
	if voice.ID != "kokoro-en" || voice.Speaker != "bm_george" {
		t.Errorf("rendered with voice %+v", voice)
	}
	if c, ok := b.Ack(); !ok || len(c.Samples) == 0 {
		t.Fatal("no ack clip")
	}

	// A new bank (a restart) finds everything on disk: no TTS calls.
	f2 := tts(t)
	b2 := clipbank.New(clipbank.Options{Dir: dir, TTS: f2})
	b2.Set(target(p, "Master Alex"))
	wait(t, b2)
	if len(f2.Spoken()) != 0 {
		t.Errorf("cache miss: rendered %v", spoken(f2))
	}
	if _, ok := b2.Ack(); !ok {
		t.Error("no ack from the cache")
	}
}

func TestAddressChangeRerendersOnlyAddressLines(t *testing.T) {
	dir := t.TempDir()
	f := tts(t)
	p := pack(t, nil)
	b := clipbank.New(clipbank.Options{Dir: dir, TTS: f})
	b.Set(target(p, "Master Alex"))
	wait(t, b)
	before := len(f.Spoken())

	b.Set(target(p, "sir"))
	wait(t, b)
	got := spoken(f)[before:]
	if len(got) != 2 || !strings.Contains(got[0]+got[1], "Right away, sir.") || !strings.Contains(got[0]+got[1], "It is done, sir.") {
		t.Errorf("re-rendered %v, want the two {address} lines only", got)
	}
	// The old personalized clips are pruned.
	if n := wavs(t, dir); n != 5 {
		t.Errorf("%d cached files after a name change, want 5", n)
	}
}

func TestVoiceChangeRerendersAll(t *testing.T) {
	dir := t.TempDir()
	f := tts(t)
	p := pack(t, nil)
	b := clipbank.New(clipbank.Options{Dir: dir, TTS: f})
	b.Set(target(p, "sir"))
	wait(t, b)
	before := len(f.Spoken())

	tg := target(p, "sir")
	tg.Voice.Speaker = "bm_lewis"
	b.Set(tg)
	wait(t, b)
	if n := len(f.Spoken()) - before; n != 5 {
		t.Errorf("re-rendered %d lines after a speaker change, want 5", n)
	}
	tg.Voice.Speed = 1.2
	b.Set(tg)
	wait(t, b)
	if n := len(f.Spoken()) - before; n != 10 {
		t.Errorf("re-rendered %d lines after a speed change, want 10 in all", n)
	}
	tg.Voice.Respell = map[string]string{"jawohl": "yah vole"}
	b.Set(tg)
	wait(t, b)
	if n := len(f.Spoken()) - before; n != 15 {
		t.Errorf("re-rendered %d lines after a respelling change, want 15 in all", n)
	}
}

// slowTTS blocks rendering of lines with {address} filled until released.
type slowTTS struct {
	speech.TTS
	release chan struct{}
	block   string
}

func (s *slowTTS) Speak(ctx context.Context, text string, v speech.Voice) (speech.AudioStream, error) {
	if strings.Contains(text, s.block) {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.TTS.Speak(ctx, text, v)
}

func TestFallbackWhileRendering(t *testing.T) {
	dir := t.TempDir()
	p := pack(t, nil)
	// Seed the cache with the static lines only, as if the user had no name.
	b0 := clipbank.New(clipbank.Options{Dir: dir, TTS: tts(t)})
	b0.Set(target(p, "sir"))
	wait(t, b0)

	slow := &slowTTS{TTS: tts(t), release: make(chan struct{}), block: "Alex"}
	b := clipbank.New(clipbank.Options{Dir: dir, TTS: slow})
	b.Set(target(p, "Master Alex"))
	defer b.Close()
	// Static lines are cached; the personalized ones are still rendering, and
	// the bank never plays them: every pick is a static line.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready := 0
		for _, e := range b.Entries() {
			if e.Ready() && !e.Personalized {
				ready++
			}
		}
		if ready == 3 { // two static acks, one working
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("static clips never became ready")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 50; i++ {
		_, text, ok := b.Pick(persona.Ack)
		if !ok || strings.Contains(text, "Alex") {
			t.Fatalf("pick %d = %q, %v while the personalized clips render", i, text, ok)
		}
	}
	for _, e := range b.Entries() {
		if e.Personalized && e.Ready() {
			t.Errorf("%+v ready before it was rendered", e)
		}
	}

	close(slow.release)
	wait(t, b)
	seen := false
	for i := 0; i < 200 && !seen; i++ {
		_, text, _ := b.Pick(persona.Ack)
		seen = strings.Contains(text, "Master Alex")
	}
	if !seen {
		t.Error("the personalized ack was never picked once rendered")
	}
}

func TestPickAvoidsRepeat(t *testing.T) {
	p := pack(t, nil)
	n := 0
	b := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: tts(t), Rand: func(k int) int { n++; return 0 }})
	b.Set(target(p, "sir"))
	wait(t, b)
	_, a, _ := b.Pick(persona.Ack)
	_, c, _ := b.Pick(persona.Ack)
	if a == c {
		t.Errorf("picked %q twice in a row", a)
	}
}

func TestShippedClips(t *testing.T) {
	var w bytes.Buffer
	clip := audio.Clip{Format: audio.Speech, Samples: []float32{0.5, 0.5, 0.5, 0.5}}
	if err := audio.EncodeWAV(&w, clip); err != nil {
		t.Fatal(err)
	}
	p := pack(t, fstest.MapFS{
		"clips/en.ack.1.wav":     {Data: w.Bytes()}, // "On it."
		"clips/en.working.0.wav": {Data: w.Bytes()},
	})
	f := tts(t)
	b := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: f})
	b.Set(target(p, "sir"))
	wait(t, b)
	got := spoken(f)
	for _, s := range got {
		if s == "On it." || s == "Working on it." {
			t.Errorf("rendered %q although the pack ships it", s)
		}
	}
	if len(got) != 3 {
		t.Errorf("rendered %v, want the other 3 lines", got)
	}
	var shipped int
	for _, e := range b.Entries() {
		if e.State == clipbank.Shipped {
			shipped++
		}
	}
	if shipped != 2 {
		t.Errorf("%d shipped, want 2", shipped)
	}
	c, _, ok := b.Pick(persona.Working)
	if !ok || len(c.Samples) != 4 {
		t.Errorf("shipped working clip = %v, %v", c, ok)
	}
}

func TestNoTTSUsesCacheOnly(t *testing.T) {
	b := clipbank.New(clipbank.Options{Dir: t.TempDir()})
	b.Set(target(pack(t, nil), "sir"))
	wait(t, b)
	if _, ok := b.Ack(); ok {
		t.Error("ack without clips")
	}
	if err := b.Render(context.Background()); err == nil {
		t.Error("Render succeeded without a TTS")
	}
}

func TestRenderFailureIsReported(t *testing.T) {
	f := tts(t)
	f.Err = os.ErrInvalid
	b := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: f, Log: slog.New(slog.DiscardHandler)})
	b.Set(target(pack(t, nil), "sir"))
	wait(t, b)
	if err := b.Render(context.Background()); err == nil {
		t.Error("expected an error")
	}
	if _, ok := b.Ack(); ok {
		t.Error("ack from nothing")
	}
}

func TestTrimsSilence(t *testing.T) {
	b := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: silent{tts(t)}})
	b.Set(target(pack(t, nil), "sir"))
	wait(t, b)
	c, ok := b.Ack()
	if !ok {
		t.Fatal("no ack")
	}
	if d := c.Duration(); d > 150*time.Millisecond {
		t.Errorf("clip is %v, silence not trimmed", d)
	}
}

// silent pads the fake's audio with a second of silence either side.
type silent struct{ speech.TTS }

func (s silent) Speak(ctx context.Context, text string, v speech.Voice) (speech.AudioStream, error) {
	st, err := s.TTS.Speak(ctx, text, v)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	var c audio.Clip
	c.Format = st.Format()
	pad := make([]float32, c.Format.SampleRate)
	c.Samples = append(c.Samples, pad...)
	for i := 0; i < 100; i++ { // 100 ms of tone
		c.Samples = append(c.Samples, 0.25)
	}
	for i := 0; i < c.Format.SampleRate/10-100; i++ {
		c.Samples = append(c.Samples, 0.25)
	}
	c.Samples = append(c.Samples, pad...)
	return audio.NewClipStream(c, 320), nil
}

func TestConcurrentSetAndPick(t *testing.T) {
	p := pack(t, nil)
	b := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: tts(t)})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				b.Set(target(p, "name"+string(rune('a'+j%3))))
				b.Ack()
				_ = b.Entries()
			}
		}()
	}
	wg.Wait()
	wait(t, b)
	b.Close()
}
