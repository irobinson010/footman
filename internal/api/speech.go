package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/speech"
)

// TranscribeParams are the params of speech.transcribe.
type TranscribeParams struct {
	// Path is an absolute path to a WAV file the daemon can read.
	Path string `json:"path"`
	// VAD splits the file into utterances with the daemon's VAD first, as
	// the microphone path does (default true). False transcribes the whole
	// file at once.
	VAD *bool `json:"vad,omitempty"`
}

// TranscribeSegment is one utterance in a speech.transcribe result.
type TranscribeSegment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
	// STTMS is how long speech-to-text took for this utterance: the delay
	// between the end of speech being detected and the text being ready.
	STTMS float64 `json:"stt_ms"`
}

// TranscribeResult is the result of speech.transcribe.
type TranscribeResult struct {
	Text       string              `json:"text"`
	Language   string              `json:"language"`
	Segments   []TranscribeSegment `json:"segments"`
	DurationMS int64               `json:"duration_ms"`
	// VADMS is the time spent in the VAD; STTMS the total speech-to-text
	// time; TotalMS the whole call, including reading the file.
	VADMS   float64 `json:"vad_ms"`
	STTMS   float64 `json:"stt_ms"`
	TotalMS float64 `json:"total_ms"`
	// STT and VAD name the engines used ("" for no VAD).
	STT string `json:"stt"`
	VAD string `json:"vad"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// speechTranscribe runs a WAV file through the daemon's own VAD and
// speech-to-text engines, outside the pipeline: nothing is typed or run, and
// no pipeline events are published. It is a diagnostic and benchmarking aid.
func (m *methods) speechTranscribe(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	start := time.Now()
	var p TranscribeParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.Path) {
		return nil, Errorf(CodeInvalidParams, "path must be absolute")
	}
	if m.d.STT == nil {
		return nil, Errorf(CodeUnavailable, "this daemon has no speech-to-text engine")
	}
	clip, err := audio.ReadWAVFile(p.Path)
	if err != nil {
		return nil, Errorf(CodeInvalidParams, "%v", err)
	}
	clip = audio.ToSpeech(clip)
	impl := m.d.Info.Implementations
	res := TranscribeResult{DurationMS: clip.Duration().Milliseconds(), STT: impl["stt"], Segments: []TranscribeSegment{}}

	useVAD := p.VAD == nil || *p.VAD
	if useVAD && m.d.VAD == nil {
		return nil, Errorf(CodeUnavailable, "this daemon has no VAD; pass \"vad\": false to transcribe the whole file")
	}
	var segs []speech.Segment
	if useVAD {
		res.VAD = impl["vad"]
		vctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// 20 ms frames, as a microphone delivers them.
		in := audio.Feed(vctx, clip.Frames(audio.Speech.SampleRate/50))
		t := time.Now()
		for seg := range m.d.VAD.Detect(vctx, in) {
			segs = append(segs, seg)
		}
		res.VADMS = ms(time.Since(t))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	} else {
		segs = []speech.Segment{{Start: 0, End: clip.Duration(), Audio: clip}}
	}

	var texts []string
	var sttTime time.Duration
	for _, s := range segs {
		t := time.Now()
		tr, err := m.d.STT.Transcribe(ctx, s.Audio)
		d := time.Since(t)
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil, err
			}
			return nil, Errorf(CodeFailed, "speech-to-text: %v", err)
		}
		sttTime += d
		if res.Language == "" {
			res.Language = tr.Language
		}
		res.Segments = append(res.Segments, TranscribeSegment{
			StartMS: s.Start.Milliseconds(), EndMS: s.End.Milliseconds(), Text: tr.Text, STTMS: ms(d),
		})
		if tr.Text != "" {
			texts = append(texts, tr.Text)
		}
	}
	res.Text = strings.Join(texts, " ")
	res.STTMS = ms(sttTime)
	res.TotalMS = ms(time.Since(start))
	return res, nil
}
