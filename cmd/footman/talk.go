package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/idle"
	"github.com/irobinson010/footman/internal/speech/models"
	"github.com/irobinson010/footman/internal/speech/sherpa"
	"github.com/irobinson010/footman/personas"
)

// The persona wiring (S3-07): the default persona's pack gives the reply
// voice, the AI's system prompt and the stock lines, and the listening
// personas' packs give the wake words. All of it is rebuilt live when the
// config changes (the persona, the user's name or title, the TTS engine).

// fallbackPersona is used when personas.default names a pack that is
// missing or broken.
const fallbackPersona = "ashford"

// ttsPersona is the speech.tts.engine value that means "the persona's
// engine".
const ttsPersona = "persona"

// personaCatalogIn is the catalog of shipped packs and those installed in
// <data>/personas.
func personaCatalogIn(dataDir string) *persona.Catalog {
	return persona.NewCatalog(personas.FS, filepath.Join(dataDir, "personas"))
}

// loadedPersona is a pack and, for a user pack, its folder (to resolve a
// voice model that is a file in the pack).
type loadedPersona struct {
	pack *persona.Pack
	dir  string
}

// findPersona loads the pack with the given ID; dir is set for user packs.
func findPersona(cat *persona.Catalog, userDir, id string) (loadedPersona, error) {
	for _, i := range cat.List() {
		if i.ID != id {
			continue
		}
		if i.Err != nil {
			return loadedPersona{}, i.Err
		}
		lp := loadedPersona{pack: i.Pack}
		if i.Source == persona.User {
			lp.dir = filepath.Join(userDir, id)
		}
		return lp, nil
	}
	return loadedPersona{}, fmt.Errorf("%w: %q", persona.ErrNotFound, id)
}

// defaultPersona loads personas.default, falling back to Ashford (with a
// log line) when it cannot be loaded. ok is false when not even that loads.
func defaultPersona(cat *persona.Catalog, userDir string, cfg *config.Config, logf func(string, ...any)) (loadedPersona, bool) {
	id := cfg.String("personas.default")
	lp, err := findPersona(cat, userDir, id)
	if err == nil {
		return lp, true
	}
	if id != fallbackPersona {
		logf("persona %q: %v; using %s", id, err, fallbackPersona)
		if lp, err = findPersona(cat, userDir, fallbackPersona); err == nil {
			return lp, true
		}
	}
	logf("no persona: %v", err)
	return loadedPersona{}, false
}

// ttsChoice is the text-to-speech engine and voice for a persona:
// speech.tts.engine = "persona" (the default) uses the pack's engine and
// voice; naming an engine uses that engine with its own default voice, at the
// pack's speed.
func ttsChoice(cfg *config.Config, lp loadedPersona) (engine string, v speech.Voice) {
	engine = cfg.String("speech.tts.engine")
	if lp.pack == nil {
		if engine == ttsPersona || engine == "" {
			engine = sherpa.KokoroName
		}
		return engine, speech.Voice{}
	}
	pv := lp.pack.Voice
	v.Speed = float32(pv.Speed)
	if engine != ttsPersona && engine != "" && engine != pv.Engine {
		return engine, v
	}
	engine = pv.Engine
	v.ID, v.Speaker = pv.Model, pv.Speaker
	if pv.ModelIsPath() {
		if lp.dir == "" {
			// A shipped pack has no folder on disk to load a file from.
			return engine, speech.Voice{Speed: v.Speed}
		}
		v.ID = filepath.Join(lp.dir, filepath.FromSlash(pv.Model))
	}
	return engine, v
}

// voiceModels are the catalog models the persona's voice needs, the
// pronunciation lexicon first: what `footman models fetch` and setup
// download. A voice that is a file in the pack needs none.
func voiceModels(cfg *config.Config, lp loadedPersona) []string {
	engine, v := ttsChoice(cfg, lp)
	id := v.ID
	if id == "" {
		switch engine {
		case sherpa.KokoroName:
			id = sherpa.DefaultKokoroModel
		case sherpa.PiperName:
			id = sherpa.DefaultPiperModel
		}
	}
	m, ok := models.Lookup(id)
	if !ok {
		return nil
	}
	return append(slices.Clone(m.Requires), m.ID)
}

// personaVoiceModels are voiceModels for personas.default (or the fallback
// persona) with the packs in dataDir.
func personaVoiceModels(cfg *config.Config, dataDir string, logf func(string, ...any)) []string {
	lp, _ := defaultPersona(personaCatalogIn(dataDir), filepath.Join(dataDir, "personas"), cfg, logf)
	return voiceModels(cfg, lp)
}

// ttsOptions are the options for the TTS engine named engine, loading v's
// model and speaker first.
func ttsOptions(engine string, dirs speechDirs, v speech.Voice) registry.Config {
	if engine != sherpa.KokoroName && engine != sherpa.PiperName {
		return nil
	}
	return registry.Map{"models_dir": dirs.models, "model": v.ID, "speaker": v.Speaker}
}

// buildPersona turns a pack into what the pipeline uses, for the user in cfg.
func buildPersona(lp loadedPersona, cfg *config.Config, voice speech.Voice, ch *persona.Chooser) core.Persona {
	p := lp.pack
	address := p.Address(cfg)
	lang := cfg.String("user.language")
	return core.Persona{
		ID: p.ID, Name: p.Name, Voice: voice,
		System: p.SystemPromptWith(core.CoreRules, p.Quips(cfg)),
		Line: func(kind core.LineKind) (string, bool) {
			l, ok := ch.Pick(p, persona.Kind(kind), lang)
			if !ok {
				return "", false
			}
			return l.Render(address), true
		},
	}
}

// personaWakeWords are the wake words the spotter listens for: those of the
// listening personas' packs and of the default persona (which answers), "hey
// footman", and speech.wake.extra (such as "alfred"). A listening persona
// whose pack cannot be loaded is listened for by its ID.
func personaWakeWords(cat *persona.Catalog, userDir string, cfg *config.Config) []string {
	var out []string
	add := func(w string) {
		if w = strings.ToLower(strings.Join(strings.Fields(w), " ")); w != "" && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	ids := slices.Clone(cfg.Strings("personas.listening"))
	if d := cfg.String("personas.default"); d != "" && !slices.Contains(ids, d) {
		ids = append(ids, d)
	}
	for _, id := range ids {
		lp, err := findPersona(cat, userDir, id)
		if err != nil || len(lp.pack.Wake.Words) == 0 {
			add(id)
			continue
		}
		for _, w := range lp.pack.Wake.Words {
			add(w)
		}
	}
	add("hey footman")
	for _, w := range cfg.Strings("speech.wake.extra") {
		add(w)
	}
	return out
}

// routerWakeWords are the words the router strips from transcripts: the
// spotter's, plus plain "footman".
func routerWakeWords(words []string) []string {
	return append(slices.Clone(words), "footman")
}

// PersonaChange is what the persona wiring tells its subscribers when the
// active persona, its voice or the user's address changes.
type PersonaChange struct {
	Pack *persona.Pack
	// Address is how the persona addresses the user now.
	Address string
	// Voice is the voice replies are spoken in.
	Voice speech.Voice
	// Config is the config it was built from.
	Config *config.Config
}

// personaPipeline is the part of the pipeline the persona wiring drives.
type personaPipeline interface {
	SetPersona(core.Persona)
	SetWakeWords([]string)
	RestartWake()
}

// personaWiring applies the default persona to the pipeline, the TTS engines
// and the wake word, at start and whenever the config changes.
type personaWiring struct {
	cat     *persona.Catalog
	userDir string
	logf    func(string, ...any)
	choose  *persona.Chooser

	// Set by the daemon before the first apply. tts and wake may be nil.
	pipeline personaPipeline
	tts      *ttsEngines
	wake     *wakeSwitch

	mu        sync.Mutex
	cur       loadedPersona
	wakeWords []string
	names     map[string]string // spoken name -> persona ID
	subs      []func(PersonaChange)
}

func newPersonaWiring(dataDir string, logf func(string, ...any)) *personaWiring {
	return &personaWiring{
		cat: personaCatalogIn(dataDir), userDir: filepath.Join(dataDir, "personas"), logf: logf,
		choose: persona.NewChooser(rand.IntN),
	}
}

// OnChange subscribes f to persona changes; it is called (synchronously,
// from apply) with the current persona at once and after every change. The
// clip bank (S3-03) re-renders its clips from here.
func (w *personaWiring) OnChange(f func(PersonaChange)) {
	w.mu.Lock()
	w.subs = append(w.subs, f)
	cur := w.cur
	w.mu.Unlock()
	if cur.pack != nil {
		f(w.change(cur, nil))
	}
}

func (w *personaWiring) change(lp loadedPersona, cfg *config.Config) PersonaChange {
	c := PersonaChange{Pack: lp.pack, Config: cfg}
	if cfg != nil {
		c.Address = lp.pack.Address(cfg)
		_, c.Voice = ttsChoice(cfg, lp)
	}
	return c
}

// load reads the default persona from cfg without applying it (for wiring
// the engines at start).
func (w *personaWiring) load(cfg *config.Config) loadedPersona {
	lp, _ := defaultPersona(w.cat, w.userDir, cfg, w.logf)
	return lp
}

// apply makes cfg's personas current: the pipeline's persona, the TTS engine,
// the wake words. Unchanged parts are left alone. It is safe to call from
// several goroutines.
func (w *personaWiring) apply(cfg *config.Config) {
	lp, ok := defaultPersona(w.cat, w.userDir, cfg, w.logf)
	engine, voice := ttsChoice(cfg, lp)
	words := personaWakeWords(w.cat, w.userDir, cfg)
	names := w.spokenNames()

	w.mu.Lock()
	defer w.mu.Unlock()
	if ok {
		if w.cur.pack == nil || w.cur.pack.ID != lp.pack.ID {
			w.logf("persona: %s (%s voice %s %s)", lp.pack.Name, engine, voice.ID, voice.Speaker)
		}
		w.cur = lp
		if w.tts != nil {
			if err := w.tts.Use(engine, voice); err != nil {
				w.logf("text-to-speech engine %s unavailable, keeping %s: %v", engine, w.tts.Current(), err)
			}
		}
		if w.pipeline != nil {
			w.pipeline.SetPersona(buildPersona(lp, cfg, voice, w.choose))
		}
	}
	w.names = names
	if !slices.Equal(words, w.wakeWords) {
		if w.pipeline != nil {
			w.pipeline.SetWakeWords(routerWakeWords(words))
		}
		if w.wake != nil && w.wakeWords != nil {
			if err := w.wake.Set(words); err != nil {
				w.logf("wake words not changed: %v", err)
			} else {
				w.logf("wake words: %s", strings.Join(words, ", "))
				if w.pipeline != nil {
					w.pipeline.RestartWake()
				}
			}
		}
		w.wakeWords = words
	}
	if ok {
		c := w.change(lp, cfg)
		for _, f := range w.subs {
			f(c)
		}
	}
}

// spokenNames maps what a persona may be called by ("klaus", "hey klaus",
// its name) to its ID, for the switch rule.
func (w *personaWiring) spokenNames() map[string]string {
	out := map[string]string{}
	for _, i := range w.cat.List() {
		if i.Err != nil {
			continue
		}
		p := i.Pack
		for _, n := range append([]string{p.ID, p.Name}, p.Wake.Words...) {
			n = strings.Join(dictation.NormWords(n), " ")
			n = strings.TrimPrefix(n, "hey ")
			if n != "" {
				out[n] = p.ID
			}
		}
	}
	return out
}

// Find implements core.PersonaSwitcher.
func (w *personaWiring) Find(name string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id, ok := w.names[name]
	return id, ok
}

// switcher is the core.PersonaSwitcher the daemon hands the switch rule:
// it sets personas.default through settings (so the change is checked and
// logged like any other) and applies it at once, so the answer comes in the
// new persona's voice.
type switcher struct {
	w        *personaWiring
	settings *core.Settings
}

func (s switcher) Find(name string) (string, bool) { return s.w.Find(name) }

func (s switcher) Switch(_ context.Context, id string) (string, error) {
	if cur := s.settings.Store.Current().String("personas.default"); cur == id {
		lp, err := findPersona(s.w.cat, s.w.userDir, id)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s is already here.", lp.pack.Name), nil
	}
	reply, st := s.settings.Set("personas.default", id)
	if st != core.Changed {
		return reply, nil
	}
	cfg := s.settings.Store.Current()
	s.w.apply(cfg)
	lp, err := findPersona(s.w.cat, s.w.userDir, id)
	if err != nil {
		return "", err
	}
	greeting := fmt.Sprintf("%s here.", lp.pack.Name)
	if l, ok := s.w.choose.Pick(lp.pack, persona.Ack, cfg.String("user.language")); ok {
		greeting += " " + l.Render(lp.pack.Address(cfg))
	}
	return greeting, nil
}

// ttsEngines is the daemon's text-to-speech: one idle-unloading engine per
// engine name, built when first needed, of which the persona's is in use.
// All the shipped personas use kokoro, so switching between them only
// changes the voice; a persona on piper builds that engine once.
type ttsEngines struct {
	// build makes the engine registered as (or standing in for) engine,
	// loading v's model, and returns the registry name it built.
	build func(engine string, v speech.Voice) (speech.TTS, string, error)
	idle  idle.Options

	mu      sync.Mutex
	holders map[string]*idle.Holder[speech.TTS] // by requested engine
	impl    map[string]string                   // requested engine -> registry name
	order   []string
	cur     string
}

func newTTSEngines(build func(string, speech.Voice) (speech.TTS, string, error), o idle.Options) *ttsEngines {
	return &ttsEngines{build: build, idle: o, holders: map[string]*idle.Holder[speech.TTS]{}, impl: map[string]string{}}
}

// Use makes engine the one replies are spoken with, building it (with v's
// model loaded first) if it has not been built yet.
func (t *ttsEngines) Use(engine string, v speech.Voice) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.holders[engine]; !ok {
		e, name, err := t.build(engine, v)
		if err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("this build has no text-to-speech engine %q (registered: %v)", engine, speech.TTSs.Names())
		}
		reload := func() (speech.TTS, error) { e, _, err := t.build(engine, v); return e, err }
		t.holders[engine] = idle.New("tts", name, e, reload, t.idle)
		t.impl[engine] = name
		t.order = append(t.order, engine)
	}
	t.cur = engine
	return nil
}

// Current is the registry name of the engine in use, or "".
func (t *ttsEngines) Current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.impl[t.cur]
}

// Speak implements speech.TTS with the engine in use.
func (t *ttsEngines) Speak(ctx context.Context, text string, v speech.Voice) (speech.AudioStream, error) {
	t.mu.Lock()
	h := t.holders[t.cur]
	t.mu.Unlock()
	if h == nil {
		return nil, errors.New("no text-to-speech engine")
	}
	return idle.TTS{H: h}.Speak(ctx, text, v)
}

// States reports every engine built, for status.
func (t *ttsEngines) States() []idle.State {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []idle.State
	for _, e := range t.order {
		out = append(out, t.holders[e].State())
	}
	return out
}

// Close closes every engine.
func (t *ttsEngines) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var err error
	for _, e := range t.order {
		err = errors.Join(err, t.holders[e].Close())
	}
	return err
}

// wakeSwitch is a wake-word engine whose words can change while running: Set
// builds an engine for the new words and swaps it in; the pipeline's next
// microphone session (RestartWake) listens with it.
type wakeSwitch struct {
	build func(words []string) (speech.WakeWord, error)

	mu    sync.Mutex
	cur   speech.WakeWord
	words []string
}

// Listen implements speech.WakeWord with the current engine.
func (w *wakeSwitch) Listen(ctx context.Context, frames <-chan speech.Frame) <-chan speech.WakeEvent {
	w.mu.Lock()
	cur := w.cur
	w.mu.Unlock()
	return cur.Listen(ctx, frames)
}

// Set builds an engine for words and makes it current. The old one is
// closed once its Listen has stopped.
func (w *wakeSwitch) Set(words []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if slices.Equal(words, w.words) {
		return nil
	}
	e, err := w.build(words)
	if err != nil {
		return err
	}
	old := w.cur
	w.cur, w.words = e, slices.Clone(words)
	go closeIfCloser(old) // the KWS waits for its Listen to end
	return nil
}

// Info describes the current engine, when it can.
func (w *wakeSwitch) Info() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if i, ok := w.cur.(engineInfo); ok {
		return i.Info()
	}
	return ""
}

// Close closes the current engine.
func (w *wakeSwitch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.cur.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
