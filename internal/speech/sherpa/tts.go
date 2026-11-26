//go:build native && cgo

package sherpa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/v2/sherpa_onnx"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/models"
)

func init() {
	for _, name := range []string{KokoroName, PiperName} {
		speech.TTSs.Register(name, func(cfg registry.Config) (speech.TTS, error) { return NewTTS(name, cfg) })
	}
}

// TTS is the sherpa-onnx text-to-speech engine, registered as "kokoro" and
// "piper" (ADR-0037). It loads Kokoro and Piper/VITS models, the stock
// voices and fine-tuned ones alike, and keeps each model it has been asked
// for loaded until Close. Synthesis runs one sentence at a time on the CPU;
// Speak calls are safe from several goroutines and are synthesized in turn.
type TTS struct {
	name string
	o    TTSOptions
	info string

	mu       sync.Mutex
	models   map[string]*ttsModel // by model file path
	lexicons map[string]lexicon   // by file path
	closed   bool
	base     context.Context // cancelled by Close
	stop     context.CancelFunc
	active   sync.WaitGroup // running syntheses

	gen sync.Mutex // one synthesis at a time
}

type ttsModel struct {
	files modelFiles
	tts   *sherpa.OfflineTts
	rate  int
	n     int      // number of speakers
	names []string // speaker names, by id
	voice string   // the model's language metadata, such as en-us
	g2p   map[accent]*g2p
}

// NewTTS builds the engine registered as name from cfg (see TTSOptions).
func NewTTS(name string, cfg registry.Config) (*TTS, error) {
	var o TTSOptions
	if err := cfg.Decode(&o); err != nil {
		return nil, err
	}
	return OpenTTS(name, o)
}

// OpenTTS loads the default model, so a missing model is reported at once.
func OpenTTS(name string, o TTSOptions) (*TTS, error) {
	o = o.withDefaults(name)
	base, stop := context.WithCancel(context.Background())
	t := &TTS{name: name, o: o, models: map[string]*ttsModel{}, lexicons: map[string]lexicon{}, base: base, stop: stop}
	t.mu.Lock()
	m, err := t.modelLocked(o.Model)
	if err == nil {
		_, err = speakerID(o.Speaker, m.names, m.n)
	}
	if err == nil {
		_, err = t.g2pLocked(m, chooseAccent("", o.Speaker, m.voice))
	}
	t.mu.Unlock()
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	t.info = fmt.Sprintf("%s, %s (%s, %d Hz", Version(), filepath.Base(m.files.dir), m.files.family, m.rate)
	if m.n > 1 {
		t.info += ", speaker " + o.Speaker
	}
	t.info += fmt.Sprintf(") on %s, %d threads", o.Provider, o.Threads)
	if o.Warmup {
		start := time.Now()
		s, err := t.Speak(context.Background(), "Ready.", speech.Voice{})
		if err == nil {
			_, err = audio.ReadAll(s)
		}
		if err != nil {
			_ = t.Close()
			return nil, err
		}
		t.info += fmt.Sprintf(", warm-up %s", time.Since(start).Round(time.Millisecond))
	}
	return t, nil
}

// Info describes the engine, for logs and status.
func (t *TTS) Info() string { return t.info }

// Close stops every synthesis in progress, waits for them and frees the
// models. It is safe to call more than once.
func (t *TTS) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.stop()
	t.mu.Unlock()
	t.active.Wait()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, m := range t.models {
		sherpa.DeleteOfflineTts(m.tts)
		delete(t.models, k)
	}
	t.lexicons = nil
	return nil
}

// Speak implements speech.TTS. v.ID picks the model (default: the engine's
// model), v.Speaker the speaker, v.Language the accent and v.Speed the rate.
// The text is normalized and cut into sentences; each is synthesized in turn
// and its audio is on the stream as soon as it is ready, in the model's own
// sample rate (mono). Cancelling ctx or closing the stream stops synthesis
// after the sentence in progress.
func (t *TTS) Speak(ctx context.Context, text string, v speech.Voice) (speech.AudioStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, speaker, speed := v.ID, v.Speaker, v.Speed
	if id == "" {
		id = t.o.Model
	}
	if speaker == "" && id == t.o.Model {
		speaker = t.o.Speaker
	}
	if speed <= 0 {
		speed = t.o.Speed
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New(t.name + ": engine closed")
	}
	m, err := t.modelLocked(id)
	var sid int
	var g *g2p
	if err == nil {
		sid, err = speakerID(speaker, m.names, m.n)
	}
	if err == nil {
		g, err = t.g2pLocked(m, chooseAccent(v.Language, speaker, m.voice))
	}
	if err != nil {
		t.mu.Unlock()
		return nil, err
	}
	t.active.Add(1)
	t.mu.Unlock()

	pieces := splitSentences(NormalizeText(text), t.o.FirstWords, maxWords)
	format := audio.Format{SampleRate: m.rate, Channels: 1}
	pause := make([]float32, int(t.o.SentencePause*float32(m.rate)))
	ctx, cancel := context.WithCancel(ctx)
	stopWithEngine := context.AfterFunc(t.base, cancel)
	return startStream(ctx, format, m.rate/50, func(ctx context.Context, emit emitFunc) error {
		defer t.active.Done()
		defer stopWithEngine()
		defer cancel()
		said := false
		for _, p := range pieces {
			if err := ctx.Err(); err != nil {
				return err
			}
			cps := g.phonemize(p)
			if len(cps) == 0 {
				continue
			}
			if said && !emit(pause) {
				return ctx.Err()
			}
			ok, err := t.synthesize(m, cps, sid, speed, emit)
			if err != nil {
				return err
			}
			if !ok {
				return ctx.Err()
			}
			said = true
		}
		return nil
	}), nil
}

// synthesize runs one piece through the model and emits its audio. It
// returns false if emit asked to stop.
func (t *TTS) synthesize(m *ttsModel, cps []rune, sid int, speed float32, emit emitFunc) (bool, error) {
	t.gen.Lock()
	defer t.gen.Unlock()
	ok := true
	cfg := &sherpa.GenerationConfig{Sid: sid, Speed: speed, PhonemeCodepoints: [][]int32{cps}}
	out := m.tts.GenerateWithConfig("", cfg, func(samples []float32, _ float32) bool {
		ok = emit(samples)
		return ok
	})
	if out == nil && ok {
		return false, fmt.Errorf("%s: sherpa-onnx failed to synthesize (see its log above)", t.name)
	}
	return ok, nil
}

// modelLocked returns the model named by id (catalog ID or path), loading it
// on first use. t.mu is held.
func (t *TTS) modelLocked(id string) (*ttsModel, error) {
	path, _, err := models.Resolve(id, t.o.ModelsDir, models.Sherpa)
	if err != nil {
		return nil, err
	}
	if m, ok := t.models[path]; ok {
		return m, nil
	}
	f, err := findModelFiles(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.name, err)
	}
	meta, err := onnxMetadata(f.model)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.name, err)
	}
	var cfg sherpa.OfflineTtsConfig
	switch f.family {
	case familyKokoro:
		// No lexicon: Footman passes phonemes (tts_g2p.go).
		cfg.Model.Kokoro = sherpa.OfflineTtsKokoroModelConfig{Model: f.model, Voices: f.voices, Tokens: f.tokens, LengthScale: 1}
	default:
		cfg.Model.Vits = sherpa.OfflineTtsVitsModelConfig{Model: f.model, Tokens: f.tokens, NoiseScale: 0.667, NoiseScaleW: 0.8, LengthScale: 1}
	}
	cfg.Model.NumThreads = t.o.Threads
	cfg.Model.Provider = t.o.Provider
	if t.o.Debug {
		cfg.Model.Debug = 1
	}
	cfg.MaxNumSentences = 1
	tts := sherpa.NewOfflineTts(&cfg)
	if tts == nil {
		return nil, fmt.Errorf("%s: sherpa-onnx could not load %s (see its log above)", t.name, f.model)
	}
	var piperConfig []byte
	if f.config != "" {
		piperConfig, _ = os.ReadFile(f.config)
	}
	m := &ttsModel{files: f, tts: tts, rate: tts.SampleRate(), n: tts.NumSpeakers(),
		names: speakerNames(meta, piperConfig), voice: meta["voice"], g2p: map[accent]*g2p{}}
	t.models[path] = m
	return m, nil
}

// g2pLocked returns the phonemizer for model m in accent acc. t.mu is held.
func (t *TTS) g2pLocked(m *ttsModel, acc accent) (*g2p, error) {
	if g, ok := m.g2p[acc]; ok {
		return g, nil
	}
	misaki := m.files.family == familyKokoro
	var paths []string
	if misaki {
		first, second := "us", "gb"
		if acc == british {
			first, second = second, first
		}
		for _, k := range []string{first, second} {
			if p := m.files.lexicons[k]; p != "" {
				paths = append(paths, p)
				break
			}
		}
	} else if p := m.files.lexicons[""]; p != "" {
		paths = append(paths, p)
	}
	var primary []lexicon
	for _, p := range paths {
		l, err := t.lexiconLocked(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.name, err)
		}
		primary = append(primary, l)
	}
	var fallback lexicon
	if lp, _, err := models.Resolve(t.o.Lexicon, t.o.ModelsDir, models.Sherpa); err == nil {
		if fallback, err = t.lexiconLocked(lp); err != nil {
			return nil, fmt.Errorf("%s: %w", t.name, err)
		}
	} else if len(primary) == 0 {
		return nil, fmt.Errorf("%s: %w for %s: %v", t.name, errNoLexicon, m.files.dir, err)
	}
	tokens, err := loadTokens(m.files.tokens)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.name, err)
	}
	g := newG2P(misaki, acc, primary, fallback, tokens)
	m.g2p[acc] = g
	return g, nil
}

func (t *TTS) lexiconLocked(path string) (lexicon, error) {
	if l, ok := t.lexicons[path]; ok {
		return l, nil
	}
	l, err := loadLexicon(path)
	if err != nil {
		return nil, err
	}
	t.lexicons[path] = l
	return l, nil
}
