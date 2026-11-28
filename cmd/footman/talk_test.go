package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/idle"
	"github.com/irobinson010/footman/internal/speech/models"
)

func TestTTSChoiceFollowsThePersona(t *testing.T) {
	cat, dir := personaCatalogIn(t.TempDir()), ""
	for id, speaker := range map[string]string{"ashford": "bm_george", "klaus": "bm_lewis", "pettigrew": "bm_fable", "lucy": "bf_emma"} {
		lp, err := findPersona(cat, dir, id)
		if err != nil {
			t.Fatal(err)
		}
		engine, v := ttsChoice(loadConfig(t, ""), lp)
		if engine != "kokoro" || v.ID != "kokoro-en" || v.Speaker != speaker || v.Speed != 1 {
			t.Errorf("%s: %s %+v", id, engine, v)
		}
		if got := voiceModels(loadConfig(t, ""), lp); !slices.Equal(got, []string{models.TTSLexicon, "kokoro-en"}) {
			t.Errorf("%s: voice models %v", id, got)
		}
	}
	// Naming an engine overrides the persona's, with that engine's voice.
	lp, _ := findPersona(cat, dir, "ashford")
	cfg := loadConfig(t, "[speech.tts]\nengine = \"piper\"\n")
	if engine, v := ttsChoice(cfg, lp); engine != "piper" || v.ID != "" || v.Speaker != "" {
		t.Errorf("override: %s %+v", engine, v)
	}
	if got := voiceModels(cfg, lp); !slices.Equal(got, []string{models.TTSLexicon, "piper-en-us-norman"}) {
		t.Errorf("override voice models %v", got)
	}
	if got := voiceModels(loadConfig(t, "[speech.tts]\nengine = \"fake\"\n"), lp); got != nil {
		t.Errorf("fake engine needs %v", got)
	}
}

func TestDefaultPersonaFallsBack(t *testing.T) {
	data := t.TempDir()
	var logged []string
	lp, ok := defaultPersona(personaCatalogIn(data), data, loadConfig(t, "[personas]\ndefault = \"nobody\"\n"),
		func(f string, a ...any) { logged = append(logged, f) })
	if !ok || lp.pack.ID != "ashford" || len(logged) != 1 {
		t.Errorf("fallback: %v %v %v", lp.pack, ok, logged)
	}
}

func TestPersonaWakeWords(t *testing.T) {
	data := t.TempDir()
	cat := personaCatalogIn(data)
	got := personaWakeWords(cat, data, loadConfig(t, ""))
	if want := []string{"ashford", "hey ashford", "hey footman"}; !slices.Equal(got, want) {
		t.Errorf("default: %v, want %v", got, want)
	}
	// The default persona listens too, and the extra words ("alfred")
	// stay; an unknown listening ID is listened for as itself.
	got = personaWakeWords(cat, data, loadConfig(t, "[personas]\ndefault = \"klaus\"\nlistening = [\"ashford\", \"jeeves\"]\n[speech.wake]\nextra = [\"Alfred\"]\n"))
	if want := []string{"ashford", "hey ashford", "jeeves", "klaus", "hey klaus", "hey footman", "alfred"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if r := routerWakeWords(got); r[len(r)-1] != "footman" || len(r) != len(got)+1 {
		t.Errorf("router words %v", r)
	}
}

type closeCounter struct {
	speech.TTS
	closed *int
}

func (c closeCounter) Close() error { *c.closed++; return nil }

// ttsEngines builds each engine once and speaks with the one in use.
func TestTTSEngines(t *testing.T) {
	var built []string
	closed := 0
	te := newTTSEngines(func(engine string, v speech.Voice) (speech.TTS, string, error) {
		if engine == "broken" {
			return nil, "", errors.New("no model")
		}
		if engine == "missing" {
			return nil, "", nil
		}
		built = append(built, engine+"/"+v.Speaker)
		return closeCounter{nil, &closed}, engine, nil
	}, idle.Options{})
	if err := te.Use("kokoro", speech.Voice{Speaker: "bm_george"}); err != nil {
		t.Fatal(err)
	}
	if err := te.Use("kokoro", speech.Voice{Speaker: "bm_lewis"}); err != nil {
		t.Fatal(err)
	}
	if err := te.Use("piper", speech.Voice{}); err != nil {
		t.Fatal(err)
	}
	if te.Use("broken", speech.Voice{}) == nil || te.Use("missing", speech.Voice{}) == nil {
		t.Error("an engine that cannot be built was accepted")
	}
	if te.Current() != "piper" {
		t.Errorf("current %q", te.Current())
	}
	if !slices.Equal(built, []string{"kokoro/bm_george", "piper/"}) {
		t.Errorf("built %v", built)
	}
	if n := len(te.States()); n != 2 {
		t.Errorf("%d states", n)
	}
	_ = te.Close()
	if closed != 2 {
		t.Errorf("closed %d", closed)
	}
}

// Warm loads an engine that was unloaded for being idle, so the first spoken
// reply after a quiet spell does not wait for the model (ADR-0051).
func TestTTSEnginesWarmReloads(t *testing.T) {
	builds := 0
	te := newTTSEngines(func(engine string, v speech.Voice) (speech.TTS, string, error) {
		builds++
		return closeCounter{nil, new(int)}, engine, nil
	}, idle.Options{})
	te.Warm(context.Background()) // nothing in use yet: no panic
	if err := te.Use("kokoro", speech.Voice{}); err != nil {
		t.Fatal(err)
	}
	te.Warm(context.Background())
	if builds != 1 {
		t.Fatalf("a loaded engine was rebuilt: %d builds", builds)
	}
	if !te.holders["kokoro"].Unload() {
		t.Fatal("could not unload")
	}
	te.Warm(context.Background())
	if builds != 2 || !te.States()[0].Loaded {
		t.Errorf("builds %d, loaded %v after Warm", builds, te.States()[0].Loaded)
	}
}

type wakeStub struct {
	words  []string
	closed chan struct{}
}

func (w *wakeStub) Listen(context.Context, <-chan speech.Frame) <-chan speech.WakeEvent { return nil }
func (w *wakeStub) Close() error                                                        { close(w.closed); return nil }

func TestWakeSwitch(t *testing.T) {
	first := &wakeStub{words: []string{"ashford"}, closed: make(chan struct{})}
	builds := 0
	ws := &wakeSwitch{cur: first, words: first.words, build: func(words []string) (speech.WakeWord, error) {
		builds++
		return &wakeStub{words: words, closed: make(chan struct{})}, nil
	}}
	if err := ws.Set([]string{"ashford"}); err != nil || builds != 0 {
		t.Fatalf("same words rebuilt: %v %d", err, builds)
	}
	if err := ws.Set([]string{"klaus"}); err != nil || builds != 1 {
		t.Fatalf("Set: %v %d", err, builds)
	}
	<-first.closed // the old engine is closed
	if got := ws.cur.(*wakeStub).words; !slices.Equal(got, []string{"klaus"}) {
		t.Errorf("current words %v", got)
	}
}
