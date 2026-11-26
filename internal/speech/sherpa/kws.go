//go:build native && cgo

package sherpa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/v2/sherpa_onnx"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/models"
)

func init() {
	speech.WakeWords.Register(KWSName, func(cfg registry.Config) (speech.WakeWord, error) { return NewKWS(cfg) })
}

// The break-glass phrases (ADR-0017) and how they are spotted. They are
// spotted only in a window after a wake word, so a stop phrase said without
// the wake word can never fire, whatever the noise (spike S0-08: 0 fires in
// 240 clips, while the same spotter run ungated hears the bare phrase 75-100%
// of the time).
var breakGlassPhrases = []string{"stop everything", "cancel everything"}

const (
	// chunkSamples is the audio fed to the spotter at a time: 100 ms at
	// 16 kHz, what the spike measured.
	chunkSamples = 1600
	// breakGlassWindow is how long after a wake word the stop phrase is
	// accepted, and replaySamples how much audio before the wake event is
	// replayed into the stop spotter: the wake word is reported about 0.4 s
	// after it ends and the phrase can follow at once.
	breakGlassWindow = 4 * time.Second
	replaySamples    = 2 * 16000
	// The stop spotter's settings: the spike found boost 1 to 2 best here.
	stopBoost     = 1
	stopThreshold = 0.25
	maxActive     = 4
)

// KWS is the wake-word engine: a sherpa-onnx keyword spotter over the wake
// words, and a second spotter for the break-glass phrases that runs only
// shortly after a wake word. One Listen runs at a time; a second waits for
// the first to stop.
type KWS struct {
	o        KWSOptions
	mu       sync.Mutex
	wake     *sherpa.KeywordSpotter
	stop     *sherpa.KeywordSpotter
	phrases  map[string]string // spotter keyword name -> phrase to report
	info     string
	closed   bool
	listenMu sync.Mutex
}

// NewKWS loads the model named in cfg (see KWSOptions).
func NewKWS(cfg registry.Config) (*KWS, error) {
	var o KWSOptions
	if err := cfg.Decode(&o); err != nil {
		return nil, err
	}
	return OpenKWS(o)
}

// OpenKWS loads the keyword-spotting model and builds the keyword lists.
func OpenKWS(o KWSOptions) (*KWS, error) {
	o = o.withDefaults()
	if o.Boost > 4 {
		return nil, fmt.Errorf("%s: boost %g is too sensitive (use 1 to 4)", KWSName, o.Boost)
	}
	dir, _, err := models.Resolve(o.Model, o.ModelsDir, models.Sherpa)
	if err != nil {
		return nil, err
	}
	tok, err := loadBPE(filepath.Join(dir, "bpe.model"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", KWSName, err)
	}
	k := &KWS{o: o, phrases: map[string]string{}}
	wakeList, err := keywordList(tok, o.Keywords, o.Boost, o.Threshold, k.phrases)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", KWSName, err)
	}
	stopList, err := keywordList(tok, breakGlassPhrases, stopBoost, stopThreshold, k.phrases)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", KWSName, err)
	}
	if k.wake, err = newSpotter(dir, o.Common, wakeList); err != nil {
		return nil, err
	}
	if k.stop, err = newSpotter(dir, o.Common, stopList); err != nil {
		sherpa.DeleteKeywordSpotter(k.wake)
		return nil, err
	}
	k.info = fmt.Sprintf("%s, %s on %s, wake words %s, boost %g, threshold %g",
		Version(), filepath.Base(dir), o.Provider, strings.Join(o.Keywords, ", "), o.Boost, o.Threshold)
	return k, nil
}

// loadBPE reads a SentencePiece model file.
func loadBPE(path string) (*bpe, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := parseBPE(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// keywordName is how a phrase is named in a keyword list: "hey footman" is
// HEY_FOOTMAN. phrases remembers the way back.
func keywordName(phrase string) string {
	return strings.ToUpper(strings.Join(strings.Fields(phrase), "_"))
}

// keywordList builds a sherpa-onnx keywords file body for phrases, with
// boost and threshold on every line, and records each phrase's name in
// phrases.
func keywordList(tok *bpe, list []string, boost, threshold float32, phrases map[string]string) (string, error) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, p := range list {
		p = strings.ToLower(strings.Join(strings.Fields(p), " "))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		pieces, err := tok.encode(p)
		if err != nil {
			return "", fmt.Errorf("keyword %q: %w", p, err)
		}
		name := keywordName(p)
		phrases[name] = p
		fmt.Fprintf(&b, "%s :%g #%g @%s\n", strings.Join(pieces, " "), boost, threshold, name)
	}
	if b.Len() == 0 {
		return "", errors.New("no keywords")
	}
	return b.String(), nil
}

func newSpotter(dir string, c Common, keywords string) (*sherpa.KeywordSpotter, error) {
	file := func(glob string) (string, error) {
		m, _ := filepath.Glob(filepath.Join(dir, glob))
		if len(m) == 0 {
			return "", fmt.Errorf("%s: no %s in %s", KWSName, glob, dir)
		}
		return m[0], nil
	}
	var p [4]string
	var err error
	// The int8 encoder and joiner, with the plain decoder: what the spike measured.
	for i, g := range []string{"encoder*.int8.onnx", "decoder*[!8].onnx", "joiner*.int8.onnx", "tokens.txt"} {
		if p[i], err = file(g); err != nil {
			return nil, err
		}
	}
	cfg := sherpa.KeywordSpotterConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: audio.Speech.SampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OnlineModelConfig{
			Transducer: sherpa.OnlineTransducerModelConfig{Encoder: p[0], Decoder: p[1], Joiner: p[2]},
			Tokens:     p[3],
			NumThreads: c.Threads,
			Provider:   c.Provider,
		},
		MaxActivePaths:  maxActive,
		KeywordsBuf:     keywords,
		KeywordsBufSize: len(keywords),
	}
	if c.Debug {
		cfg.ModelConfig.Debug = 1
	}
	sp := sherpa.NewKeywordSpotter(&cfg)
	if sp == nil {
		return nil, fmt.Errorf("%s: sherpa-onnx could not load %s (see its log above)", KWSName, dir)
	}
	return sp, nil
}

// Info describes the engine, for logs and status.
func (k *KWS) Info() string { return k.info }

// Close frees the model. It waits for a running Listen to finish, so cancel
// its context first. It is safe to call more than once.
func (k *KWS) Close() error {
	k.listenMu.Lock()
	defer k.listenMu.Unlock()
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.closed {
		k.closed = true
		sherpa.DeleteKeywordSpotter(k.wake)
		sherpa.DeleteKeywordSpotter(k.stop)
	}
	return nil
}

// Listen implements speech.WakeWord. Frames may be any format and size; they
// are converted to 16 kHz mono and fed to the spotter in 100 ms chunks.
// Events carry offsets measured from the first frame's Offset. When frames
// closes, the audio still held is flushed so a word at the very end counts.
func (k *KWS) Listen(ctx context.Context, frames <-chan speech.Frame) <-chan speech.WakeEvent {
	out := make(chan speech.WakeEvent)
	go func() {
		defer close(out)
		k.listenMu.Lock()
		defer k.listenMu.Unlock()
		k.mu.Lock()
		closed := k.closed
		k.mu.Unlock()
		if closed {
			return
		}
		d := newDetector(k)
		defer d.close()
		var (
			base    time.Duration
			started bool
			buf     []float32
		)
		send := func(evs []speech.WakeEvent) bool {
			for _, e := range evs {
				e.At += base
				select {
				case out <- e:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		for {
			var f speech.Frame
			var ok bool
			select {
			case f, ok = <-frames:
			case <-ctx.Done():
				return
			}
			if !ok {
				// The last words need trailing silence to be reported.
				buf = append(buf, make([]float32, chunkSamples*6)...)
				for len(buf) >= chunkSamples {
					if !send(d.feed(buf[:chunkSamples])) {
						return
					}
					buf = buf[chunkSamples:]
				}
				return
			}
			if !started {
				base, started = f.Offset, true
			}
			samples := f.Samples
			if f.Format != audio.Speech {
				samples = audio.ToSpeech(audio.Clip{Format: f.Format, Samples: f.Samples}).Samples
			}
			buf = append(buf, samples...)
			for len(buf) >= chunkSamples {
				if !send(d.feed(buf[:chunkSamples])) {
					return
				}
				buf = buf[chunkSamples:]
			}
			if len(buf) == 0 {
				buf = nil
			}
		}
	}()
	return out
}

// detector is the state of one Listen: the wake stream, and the stop stream
// while its window is open.
type detector struct {
	k        *KWS
	ws, ss   *sherpa.OnlineStream
	pos      int // samples fed so far
	ssUntil  int // sample position where the stop window closes
	ring     []float32
	windowNs int
}

func newDetector(k *KWS) *detector {
	return &detector{k: k, ws: sherpa.NewKeywordStream(k.wake),
		windowNs: audio.Speech.SampleRate * int(breakGlassWindow/time.Second)}
}

func (d *detector) close() {
	sherpa.DeleteOnlineStream(d.ws)
	d.closeStop()
}

func (d *detector) closeStop() {
	if d.ss != nil {
		sherpa.DeleteOnlineStream(d.ss)
		d.ss = nil
	}
}

// spot decodes what is ready on s and returns the keyword names found. The
// stream is reset after each, so one utterance does not fire twice.
func spot(sp *sherpa.KeywordSpotter, s *sherpa.OnlineStream) []string {
	var found []string
	for sp.IsReady(s) {
		sp.Decode(s)
		if r := sp.GetResult(s); r.Keyword != "" {
			found = append(found, strings.TrimSpace(r.Keyword))
			sp.Reset(s)
		}
	}
	return found
}

// feed processes one chunk and returns the events it produced, with offsets
// from the start of the audio.
func (d *detector) feed(chunk []float32) []speech.WakeEvent {
	rate := audio.Speech.SampleRate
	d.pos += len(chunk)
	at := audio.Speech.Duration(d.pos)
	d.ring = append(d.ring, chunk...)
	if len(d.ring) > replaySamples {
		d.ring = d.ring[len(d.ring)-replaySamples:]
	}
	var evs []speech.WakeEvent
	if d.ss != nil {
		if d.pos > d.ssUntil {
			d.closeStop()
		} else {
			d.ss.AcceptWaveform(rate, chunk)
			if found := spot(d.k.stop, d.ss); len(found) > 0 {
				evs = append(evs, speech.WakeEvent{Kind: speech.WakeKindBreakGlass, Keyword: d.k.phrases[found[0]], At: at})
				d.closeStop() // once per wake word
			}
		}
	}
	d.ws.AcceptWaveform(rate, chunk)
	for _, name := range spot(d.k.wake, d.ws) {
		evs = append(evs, speech.WakeEvent{Kind: speech.WakeKindWake, Keyword: d.k.phrases[name], At: at})
		if d.ss == nil {
			d.ss = sherpa.NewKeywordStream(d.k.stop)
			d.ss.AcceptWaveform(rate, d.ring)
		}
		d.ssUntil = d.pos + d.windowNs
	}
	return evs
}
