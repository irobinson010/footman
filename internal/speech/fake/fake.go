// Package fake provides deterministic, scriptable speech engines for tests.
// Importing it registers each engine under the name "fake" in the speech
// registries:
//
//	import _ "github.com/irobinson010/footman/internal/speech/fake"
//
//	stt, _ := speech.STTs.New("fake", registry.Map{"transcripts": []string{"open youtube"}})
//	stt.(*fake.STT).Queue(speech.Transcript{Text: "scratch that"})
package fake

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
)

// Name is the registry name of every fake engine.
const Name = "fake"

func init() {
	speech.VADs.Register(Name, func(cfg registry.Config) (speech.VAD, error) { return NewVAD(cfg) })
	speech.WakeWords.Register(Name, func(cfg registry.Config) (speech.WakeWord, error) { return NewWakeWord(cfg) })
	speech.STTs.Register(Name, func(cfg registry.Config) (speech.STT, error) { return NewSTT(cfg) })
	speech.TTSs.Register(Name, func(cfg registry.Config) (speech.TTS, error) { return NewTTS(cfg) })
}

// VAD is an energy-threshold voice activity detector: a frame whose peak
// absolute sample reaches Threshold is speech, anything else is silence. A
// segment ends after Silence consecutive silent frames.
type VAD struct {
	// Threshold is the peak level that counts as speech (default 0.1).
	Threshold float32 `json:"threshold"`
	// Silence is how many silent frames end a segment (default 1).
	Silence int `json:"silence"`
}

// NewVAD builds a VAD from cfg (keys "threshold" and "silence").
func NewVAD(cfg registry.Config) (*VAD, error) {
	v := &VAD{Threshold: 0.1, Silence: 1}
	if err := cfg.Decode(v); err != nil {
		return nil, err
	}
	if v.Silence < 1 {
		v.Silence = 1
	}
	return v, nil
}

// Detect implements speech.VAD.
func (v *VAD) Detect(ctx context.Context, frames <-chan speech.Frame) <-chan speech.Segment {
	out := make(chan speech.Segment)
	go func() {
		defer close(out)
		var (
			seg     *speech.Segment
			silent  int
			emitSeg = func() bool {
				s := *seg
				seg = nil
				select {
				case out <- s:
					return true
				case <-ctx.Done():
					return false
				}
			}
		)
		for {
			var f speech.Frame
			var ok bool
			select {
			case f, ok = <-frames:
			case <-ctx.Done():
				return
			}
			if !ok {
				if seg != nil {
					emitSeg()
				}
				return
			}
			if peak(f.Samples) >= v.Threshold {
				if seg == nil {
					seg = &speech.Segment{Start: f.Offset, Audio: speech.Audio{Format: f.Format}}
				}
				seg.Audio.Samples = append(seg.Audio.Samples, f.Samples...)
				seg.End = f.Offset + f.Duration()
				silent = 0
				continue
			}
			if seg == nil {
				continue
			}
			silent++
			if silent >= v.Silence {
				silent = 0
				if !emitSeg() {
					return
				}
			}
		}
	}()
	return out
}

func peak(s []float32) float32 {
	var p float32
	for _, x := range s {
		p = max(p, float32(math.Abs(float64(x))))
	}
	return p
}

// WakeWord fires Keyword after the frames at the scheduled indices (counted
// from 0 on each call to Listen).
type WakeWord struct {
	mu sync.Mutex
	// Keyword is reported in each event (default "footman").
	Keyword string `json:"keyword"`
	// At lists the frame indices after which the wake word fires.
	At []int `json:"at"`
}

// NewWakeWord builds a WakeWord from cfg (keys "keyword" and "at").
func NewWakeWord(cfg registry.Config) (*WakeWord, error) {
	w := &WakeWord{Keyword: "footman"}
	if err := cfg.Decode(w); err != nil {
		return nil, err
	}
	return w, nil
}

// Schedule adds frame indices after which the wake word fires on later calls
// to Listen.
func (w *WakeWord) Schedule(at ...int) {
	w.mu.Lock()
	w.At = append(w.At, at...)
	w.mu.Unlock()
}

// Listen implements speech.WakeWord.
func (w *WakeWord) Listen(ctx context.Context, frames <-chan speech.Frame) <-chan speech.WakeEvent {
	w.mu.Lock()
	at := map[int]bool{}
	for _, i := range w.At {
		at[i] = true
	}
	kw := w.Keyword
	w.mu.Unlock()

	out := make(chan speech.WakeEvent)
	go func() {
		defer close(out)
		for i := 0; ; i++ {
			var f speech.Frame
			var ok bool
			select {
			case f, ok = <-frames:
			case <-ctx.Done():
				return
			}
			if !ok {
				return
			}
			if !at[i] {
				continue
			}
			select {
			case out <- speech.WakeEvent{Keyword: kw, At: f.Offset + f.Duration(), Score: 1}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// ErrNoTranscript is returned by STT.Transcribe when its queue is empty.
var ErrNoTranscript = errors.New("fake stt: no transcript queued")

// STT returns queued transcripts in order, one per call, and records the
// audio it was given.
type STT struct {
	mu     sync.Mutex
	queue  []speech.Transcript
	inputs []speech.Audio
}

// NewSTT builds an STT from cfg (key "transcripts": a list of texts to queue).
func NewSTT(cfg registry.Config) (*STT, error) {
	var opts struct {
		Transcripts []string `json:"transcripts"`
	}
	if err := cfg.Decode(&opts); err != nil {
		return nil, err
	}
	s := &STT{}
	for _, t := range opts.Transcripts {
		s.Queue(speech.Transcript{Text: t, Language: "en", Confidence: 1})
	}
	return s, nil
}

// Queue appends transcripts to be returned by later calls.
func (s *STT) Queue(ts ...speech.Transcript) {
	s.mu.Lock()
	s.queue = append(s.queue, ts...)
	s.mu.Unlock()
}

// Inputs returns the audio passed to each call so far.
func (s *STT) Inputs() []speech.Audio {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]speech.Audio(nil), s.inputs...)
}

// Transcribe implements speech.STT.
func (s *STT) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return speech.Transcript{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, a)
	if len(s.queue) == 0 {
		return speech.Transcript{}, ErrNoTranscript
	}
	t := s.queue[0]
	s.queue = s.queue[1:]
	return t, nil
}

// Utterance is one call to TTS.Speak.
type Utterance struct {
	// Text that was spoken.
	Text string
	// Voice it was spoken in.
	Voice speech.Voice
}

// TTS synthesizes a constant tone whose length is proportional to the text
// (PerChar per character), and records what it was asked to say.
type TTS struct {
	mu     sync.Mutex
	spoken []Utterance

	// PerChar is the audio length per character of text (default 10ms).
	PerChar time.Duration `json:"per_char"`
	// FrameSize is the number of samples per streamed frame (default 320).
	FrameSize int `json:"frame_size"`
	// Err, if set, is returned by every call to Speak.
	Err error `json:"-"`
}

// NewTTS builds a TTS from cfg (keys "per_char" in nanoseconds and
// "frame_size").
func NewTTS(cfg registry.Config) (*TTS, error) {
	t := &TTS{PerChar: 10 * time.Millisecond, FrameSize: 320}
	if err := cfg.Decode(t); err != nil {
		return nil, err
	}
	if t.FrameSize < 1 {
		t.FrameSize = 320
	}
	return t, nil
}

// Spoken returns every utterance so far, in order.
func (t *TTS) Spoken() []Utterance {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Utterance(nil), t.spoken...)
}

// Speak implements speech.TTS.
func (t *TTS) Speak(ctx context.Context, text string, v speech.Voice) (speech.AudioStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Err != nil {
		return nil, t.Err
	}
	t.spoken = append(t.spoken, Utterance{Text: text, Voice: v})
	f := audio.Speech
	n := int(time.Duration(len([]rune(text))) * t.PerChar * time.Duration(f.SampleRate) / time.Second)
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = 0.25
	}
	return audio.NewClipStream(audio.Clip{Format: f, Samples: samples}, t.FrameSize), nil
}
