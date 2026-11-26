//go:build native && unix

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/internal/persona/clipbank"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/idle"
	"github.com/irobinson010/footman/internal/speech/sherpa"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

// TestTalkLatency measures S3-07's numbers with the real CPU engines and the
// fake platform (nothing is played; the fake player takes the audio): the
// time from the end of speech to the first acknowledgement sample and to the
// first reply audio, for a fast-path command and an AI command (the fake AI
// answers at once, so this is the voice's share). The commands are spoken
// by Kokoro and transcribed by Parakeet on the CPU. It runs only when
// FOOTMAN_TALK_RUNS is set (the number of runs per command); with
// FOOTMAN_TALK_WAVS set to a folder, the played audio is written there.
func TestTalkLatency(t *testing.T) {
	runs, _ := strconv.Atoi(os.Getenv("FOOTMAN_TALK_RUNS"))
	if runs <= 0 {
		t.Skip("set FOOTMAN_TALK_RUNS to measure")
	}
	dir := speechtest.ModelsDir(t, "kokoro-en", "tts-lexicon-en-us", "parakeet-tdt-0.6b-v2")
	ctx := context.Background()
	cfg := loadConfig(t, "")
	lp, err := findPersona(personaCatalogIn(t.TempDir()), "", "ashford")
	if err != nil {
		t.Fatal(err)
	}
	engine, voice := ttsChoice(cfg, lp)
	dirs := speechDirs{models: dir}

	start := time.Now()
	tts := newTTSEngines(func(e string, v speech.Voice) (speech.TTS, string, error) {
		x, err := speech.TTSs.New(e, ttsOptions(e, dirs, v))
		return x, e, err
	}, idle.Options{After: func() time.Duration { return 0 }})
	if err := tts.Use(engine, voice); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tts.Close() }()
	t.Logf("kokoro load: %s", time.Since(start).Round(time.Millisecond))
	stt, err := speech.STTs.New(sherpa.ParakeetName, registry.Map{"model": "parakeet-tdt-0.6b-v2", "models_dir": dir, "accelerator": "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeIfCloser(stt)

	// The clip bank, rendered with the same engine.
	start = time.Now()
	bank := clipbank.New(clipbank.Options{Dir: t.TempDir(), TTS: tts})
	defer bank.Close()
	bank.Set(clipbank.Target{Pack: lp.pack, Voice: voice, Address: lp.pack.Address(cfg), Lang: "en"})
	if err := bank.Render(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("clip bank: %d clips rendered in %s", len(bank.Entries()), time.Since(start).Round(time.Millisecond))

	// What the owner says, in another voice, at 16 kHz.
	say := func(text string) speech.Audio {
		s, err := tts.Speak(ctx, text, speech.Voice{ID: "kokoro-en", Speaker: "am_michael"})
		if err != nil {
			t.Fatal(err)
		}
		c, err := audio.ReadAll(s)
		if err != nil {
			t.Fatal(err)
		}
		return audio.Resample(c, audio.Speech.SampleRate)
	}

	b := bus.New()
	defer b.Close()
	plat, err := pfake.New(registry.Map{"root": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ai, _ := lfake.New(registry.Map(nil))
	p, err := core.New(core.Deps{Bus: b, Platform: plat, STT: stt, TTS: tts, LLM: ai}, core.Config{
		Ack: bank, Speak: core.SpeakAlways, Notify: true, WakeWords: routerWakeWords(personaWakeWords(personaCatalogIn(t.TempDir()), "", cfg)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.SetPersona(buildPersona(lp, cfg, voice, persona.NewChooser(func(int) int { return 0 })))

	acks := bus.Subscribe(b, core.Acknowledged, 64)
	defer acks.Close()
	speaks := bus.Subscribe(b, core.SpeechStarted, 64)
	defer speaks.Close()
	wavs := os.Getenv("FOOTMAN_TALK_WAVS")

	for _, c := range []struct{ name, said, reply string }{
		{"fast path", "Ashford, open YouTube.", ""},
		{"AI", "Ashford, what is the weather like?", "It is cold and grey, sir. I would take a coat."},
	} {
		utter := say(c.said)
		var sttT, ackT, replyT []time.Duration
		for i := range runs + 1 { // the first run warms up and is not counted
			if c.reply != "" {
				ai.Queue(lfake.Reply{Text: c.reply})
			}
			ended := time.Now()
			tr, err := stt.Transcribe(ctx, utter)
			if err != nil {
				t.Fatal(err)
			}
			took := time.Since(ended)
			submitted := time.Since(ended)
			if err := p.Submit(ctx, core.Utterance{Trigger: core.TriggerWake, Text: tr.Text, Ended: ended}); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			a := next(t, acks)
			s := next(t, speaks)
			if i == 0 {
				t.Logf("%s: heard %q", c.name, tr.Text)
				continue
			}
			sttT = append(sttT, took)
			ackT = append(ackT, a.Latency)
			replyT = append(replyT, submitted+s.After)
		}
		t.Logf("%s, %d runs: speech-to-text %s; end of speech -> first ack sample %s; -> first reply audio %s",
			c.name, runs, stats(sttT), stats(ackT), stats(replyT))
	}
	if wavs != "" {
		for i, clip := range plat.Audio().(*pfake.Audio).Played() {
			f, err := os.Create(filepath.Join(wavs, fmt.Sprintf("played-%02d.wav", i)))
			if err != nil {
				t.Fatal(err)
			}
			_ = audio.EncodeWAV(f, clip)
			_ = f.Close()
		}
	}
}

func next[T any](t *testing.T, s *bus.Subscription[T]) T {
	t.Helper()
	select {
	case ev := <-s.C():
		return ev.Data
	case <-time.After(30 * time.Second):
		t.Fatal("no event")
	}
	panic("unreachable")
}

// stats is "median X (min Y, max Z)" in milliseconds.
func stats(ds []time.Duration) string {
	s := slices.Clone(ds)
	slices.Sort(s)
	ms := func(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
	return fmt.Sprintf("median %s ms (min %s, max %s)", ms(s[len(s)/2]), ms(s[0]), ms(s[len(s)-1]))
}
