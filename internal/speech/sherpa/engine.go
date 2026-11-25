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
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/models"
)

func init() {
	speech.STTs.Register(ParakeetName, func(cfg registry.Config) (speech.STT, error) { return NewParakeet(cfg) })
	speech.VADs.Register(SileroName, func(cfg registry.Config) (speech.VAD, error) { return NewSilero(cfg) })
	accel.RegisterEngine(ParakeetName, func() []string { return []string{accel.CPU} })
}

// Version reports the sherpa-onnx and onnxruntime versions linked in.
func Version() string {
	return "sherpa-onnx " + sherpa.GetVersion() + ", onnxruntime " + sherpa.GetOnnxruntimeVersion()
}

// Parakeet is the Parakeet TDT speech-to-text engine. It is safe for
// concurrent use; transcriptions run one at a time.
type Parakeet struct {
	mu   sync.Mutex
	rec  *sherpa.OfflineRecognizer
	info string
	warm time.Duration
}

// NewParakeet loads the model named in cfg (see ParakeetOptions).
func NewParakeet(cfg registry.Config) (*Parakeet, error) {
	var o ParakeetOptions
	if err := cfg.Decode(&o); err != nil {
		return nil, err
	}
	return OpenParakeet(o)
}

// OpenParakeet loads a Parakeet model.
func OpenParakeet(o ParakeetOptions) (*Parakeet, error) {
	o = o.withDefaults()
	if a := o.Accelerator; a != "" && a != "auto" && a != accel.CPU {
		return nil, fmt.Errorf("%s runs on the CPU only, not %q", ParakeetName, a)
	}
	if l := strings.ToLower(o.Language); l != "" && l != "en" && !strings.HasPrefix(l, "en-") && !strings.HasPrefix(l, "en_") {
		return nil, fmt.Errorf("%s is English-only; language %q needs whispercpp", ParakeetName, o.Language)
	}
	dir, _, err := models.Resolve(o.Model, o.ModelsDir, models.Sherpa)
	if err != nil {
		return nil, err
	}
	file := func(glob string) (string, error) {
		m, _ := filepath.Glob(filepath.Join(dir, glob))
		if len(m) == 0 {
			return "", fmt.Errorf("%s: no %s in %s", ParakeetName, glob, dir)
		}
		return m[0], nil
	}
	var paths [4]string
	for i, g := range []string{"encoder*.onnx", "decoder*.onnx", "joiner*.onnx", "tokens.txt"} {
		if paths[i], err = file(g); err != nil {
			return nil, err
		}
	}
	cfg := sherpa.OfflineRecognizerConfig{
		FeatConfig:     sherpa.FeatureConfig{SampleRate: audio.Speech.SampleRate, FeatureDim: 80},
		DecodingMethod: "greedy_search",
	}
	cfg.ModelConfig.Transducer = sherpa.OfflineTransducerModelConfig{Encoder: paths[0], Decoder: paths[1], Joiner: paths[2]}
	cfg.ModelConfig.Tokens = paths[3]
	cfg.ModelConfig.ModelType = "nemo_transducer"
	cfg.ModelConfig.NumThreads = o.Threads
	cfg.ModelConfig.Provider = o.Provider
	if o.Debug {
		cfg.ModelConfig.Debug = 1
	}
	rec := sherpa.NewOfflineRecognizer(&cfg)
	if rec == nil {
		return nil, fmt.Errorf("%s: sherpa-onnx could not load %s (see its log above)", ParakeetName, dir)
	}
	p := &Parakeet{rec: rec, info: fmt.Sprintf("%s, %s on %s, %d threads", Version(), filepath.Base(dir), o.Provider, o.Threads)}
	if o.Warmup {
		start := time.Now()
		if _, err := p.Transcribe(context.Background(), speech.Audio{Format: audio.Speech, Samples: make([]float32, audio.Speech.SampleRate)}); err != nil {
			_ = p.Close()
			return nil, err
		}
		p.warm = time.Since(start)
	}
	return p, nil
}

// Transcribe implements speech.STT. Audio in another format is converted to
// 16 kHz mono first. ctx is checked before decoding starts; a decode in
// progress runs to the end (sherpa-onnx cannot abort one).
func (p *Parakeet) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return speech.Transcript{}, err
	}
	a = audio.ToSpeech(a)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rec == nil {
		return speech.Transcript{}, errors.New(ParakeetName + ": engine closed")
	}
	if len(a.Samples) == 0 {
		return speech.Transcript{Language: "en"}, nil
	}
	s := sherpa.NewOfflineStream(p.rec)
	defer sherpa.DeleteOfflineStream(s)
	s.AcceptWaveform(a.Format.SampleRate, a.Samples)
	p.rec.Decode(s)
	if err := ctx.Err(); err != nil {
		return speech.Transcript{}, err
	}
	r := s.GetResult()
	return speech.Transcript{Text: strings.TrimSpace(r.Text), Language: "en"}, nil
}

// Info describes the loaded engine, for logs and status.
func (p *Parakeet) Info() string {
	if p.warm > 0 {
		return p.info + fmt.Sprintf(", warm-up %s", p.warm.Round(time.Millisecond))
	}
	return p.info
}

// Close frees the model. It is safe to call more than once.
func (p *Parakeet) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rec != nil {
		sherpa.DeleteOfflineRecognizer(p.rec)
		p.rec = nil
	}
	return nil
}

// Silero is the Silero voice activity detector. Each Detect call runs its own
// detector, so calls are independent.
type Silero struct {
	o     SileroOptions
	model string
}

// NewSilero checks the model named in cfg (see SileroOptions). The model is
// loaded by each Detect call.
func NewSilero(cfg registry.Config) (*Silero, error) {
	var o SileroOptions
	if err := cfg.Decode(&o); err != nil {
		return nil, err
	}
	return OpenSilero(o)
}

// OpenSilero returns a Silero VAD after checking that its model loads.
func OpenSilero(o SileroOptions) (*Silero, error) {
	o = o.withDefaults()
	path, _, err := models.Resolve(o.Model, o.ModelsDir, models.Sherpa)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return nil, fmt.Errorf("%s: %s is not a model file", SileroName, path)
	}
	v := &Silero{o: o, model: path}
	d, err := v.newDetector()
	if err != nil {
		return nil, err
	}
	sherpa.DeleteVoiceActivityDetector(d)
	return v, nil
}

// Info describes the VAD, for logs and status.
func (v *Silero) Info() string {
	return fmt.Sprintf("Silero VAD (%s), threshold %.2f, ends speech after %.2f s of silence",
		Version(), v.o.Threshold, v.o.MinSilence)
}

func (v *Silero) newDetector() (*sherpa.VoiceActivityDetector, error) {
	cfg := sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              v.model,
			Threshold:          v.o.Threshold,
			MinSilenceDuration: v.o.MinSilence,
			MinSpeechDuration:  v.o.MinSpeech,
			MaxSpeechDuration:  v.o.MaxSpeech,
			WindowSize:         sileroWindow,
		},
		SampleRate: audio.Speech.SampleRate,
		NumThreads: v.o.Threads,
		Provider:   v.o.Provider,
	}
	if v.o.Debug {
		cfg.Debug = 1
	}
	// The buffer holds the speech of the segment being built; it must
	// exceed MaxSpeech.
	d := sherpa.NewVoiceActivityDetector(&cfg, v.o.MaxSpeech+5)
	if d == nil {
		return nil, fmt.Errorf("%s: sherpa-onnx could not load %s", SileroName, v.model)
	}
	return d, nil
}

// Detect implements speech.VAD. Frames should be 16 kHz mono (others are
// converted frame by frame). Segment offsets are measured from the first
// frame's Offset.
func (v *Silero) Detect(ctx context.Context, frames <-chan speech.Frame) <-chan speech.Segment {
	out := make(chan speech.Segment)
	go func() {
		defer close(out)
		d, err := v.newDetector()
		if err != nil {
			// The model loaded in OpenSilero, so this is unexpected; with no
			// error channel, log it and drain nothing.
			fmt.Fprintln(os.Stderr, "footman:", err)
			return
		}
		defer sherpa.DeleteVoiceActivityDetector(d)

		var (
			base    time.Duration
			started bool
			buf     []float32
		)
		emit := func() bool {
			for !d.IsEmpty() {
				s := d.Front()
				d.Pop()
				start := base + audio.Speech.Duration(int(s.Start))
				seg := speech.Segment{
					Start: start,
					End:   start + audio.Speech.Duration(len(s.Samples)),
					Audio: speech.Audio{Format: audio.Speech, Samples: s.Samples},
				}
				select {
				case out <- seg:
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
				if len(buf) > 0 {
					d.AcceptWaveform(buf)
				}
				d.Flush()
				emit()
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
			n := len(buf) - len(buf)%sileroWindow
			if n == 0 {
				continue
			}
			d.AcceptWaveform(buf[:n])
			buf = append(buf[:0], buf[n:]...)
			if !emit() {
				return
			}
		}
	}()
	return out
}
