// Package sherpa holds the sherpa-onnx speech engines (ADR-0024):
//
//   - "sherpa-parakeet" in speech.STTs: NVIDIA Parakeet TDT 0.6B v2, an
//     English transducer that is the default on machines without a GPU.
//   - "silero" in speech.VADs: Silero VAD, which splits microphone audio
//     into utterances.
//   - "sherpa" in speech.WakeWords: keyword spotting for the wake words and
//     the break-glass stop phrase.
//
// They share one wrapper around the official sherpa-onnx Go module, which
// ships prebuilt onnxruntime and sherpa-onnx libraries (CPU) for Linux,
// macOS and Windows.
// The engines need cgo, so they are compiled only with the `native` build
// tag. This file holds the options, which need no cgo.
package sherpa

import (
	"runtime"
)

// Registry names.
const (
	// ParakeetName is the Parakeet STT engine's name.
	ParakeetName = "sherpa-parakeet"
	// SileroName is the Silero VAD's name.
	SileroName = "silero"
	// KWSName is the keyword-spotting wake-word engine's name.
	KWSName = "sherpa"
)

// Common are the options every sherpa engine takes.
type Common struct {
	// ModelsDir is the models folder catalog IDs are looked up in.
	ModelsDir string `json:"models_dir"`
	// Threads is the number of CPU threads (0 picks a default).
	Threads int `json:"threads"`
	// Provider is the onnxruntime execution provider (default "cpu"; "cuda"
	// needs the separate GPU runtime, which is not shipped).
	Provider string `json:"provider"`
	// Debug makes sherpa-onnx log its configuration.
	Debug bool `json:"debug"`
}

func (c Common) withDefaults(threads int) Common {
	if c.Threads <= 0 {
		c.Threads = threads
	}
	if c.Provider == "" {
		c.Provider = "cpu"
	}
	return c
}

// ParakeetOptions configure the Parakeet STT engine.
type ParakeetOptions struct {
	Common
	// Model is a model catalog ID or a path to the model folder (with
	// encoder, decoder and joiner .onnx files and tokens.txt). Default
	// parakeet-tdt-0.6b-v2.
	Model string `json:"model"`
	// Accelerator is accepted for symmetry with the other engines; only
	// "cpu" (or auto) is supported.
	Accelerator string `json:"accelerator"`
	// Language must be English (or empty): Parakeet v2 is English-only.
	Language string `json:"language"`
	// CacheDir is accepted and unused.
	CacheDir string `json:"cache_dir"`
	// Warmup runs one short transcription at load (default false).
	Warmup bool `json:"warmup"`
}

func (o ParakeetOptions) withDefaults() ParakeetOptions {
	if o.Model == "" {
		o.Model = "parakeet-tdt-0.6b-v2"
	}
	o.Common = o.Common.withDefaults(max(1, min(16, runtime.NumCPU()/2)))
	return o
}

// SileroOptions configure the Silero VAD.
type SileroOptions struct {
	Common
	// Model is a model catalog ID or a path to silero_vad.onnx. Default
	// silero-vad.
	Model string `json:"model"`
	// Threshold is the speech probability that counts as speech (default
	// 0.5).
	Threshold float32 `json:"threshold"`
	// MinSilence is how long a pause ends a segment, in seconds (default
	// 0.5; the wake-word spike used 0.25, which splits dictation at short
	// pauses).
	MinSilence float32 `json:"min_silence"`
	// MinSpeech is the shortest speech that makes a segment, in seconds
	// (default 0.25).
	MinSpeech float32 `json:"min_speech"`
	// MaxSpeech splits longer speech, in seconds (default 30).
	MaxSpeech float32 `json:"max_speech"`
}

func (o SileroOptions) withDefaults() SileroOptions {
	if o.Model == "" {
		o.Model = "silero-vad"
	}
	if o.Threshold <= 0 {
		o.Threshold = 0.5
	}
	if o.MinSilence <= 0 {
		o.MinSilence = 0.5
	}
	if o.MinSpeech <= 0 {
		o.MinSpeech = 0.25
	}
	if o.MaxSpeech <= 0 {
		o.MaxSpeech = 30
	}
	o.Common = o.Common.withDefaults(1)
	return o
}

// sileroWindow is the number of 16 kHz samples Silero VAD takes per step.
const sileroWindow = 512

// KWS defaults (spike S0-08: boost is the sensitivity knob, 2 is the
// recommended value, and above 4 false wakes appear).
const (
	// DefaultBoost is speech.wake.boost.
	DefaultBoost = 2
	// DefaultThreshold is speech.wake.threshold.
	DefaultThreshold = 0.05
	// DefaultKWSModel is the keyword-spotting model's catalog ID.
	DefaultKWSModel = "kws-zipformer-gigaspeech"
)

// KWSOptions configure the keyword-spotting wake-word engine.
type KWSOptions struct {
	Common
	// Model is a model catalog ID or a path to the model folder. Default
	// kws-zipformer-gigaspeech.
	Model string `json:"model"`
	// Keywords are the wake words to listen for, such as "ashford" or
	// "hey footman" (default just "hey footman"). Each also opens the
	// break-glass window.
	Keywords []string `json:"keywords"`
	// Boost is the score added to each token of a keyword: higher is more
	// sensitive and gives more false wakes (default 2; keep it 1 to 4).
	Boost float32 `json:"boost"`
	// Threshold is the score a keyword must reach (default 0.05; changing
	// it matters little, use Boost).
	Threshold float32 `json:"threshold"`
}

func (o KWSOptions) withDefaults() KWSOptions {
	if o.Model == "" {
		o.Model = DefaultKWSModel
	}
	if len(o.Keywords) == 0 {
		o.Keywords = []string{"hey footman"}
	}
	if o.Boost <= 0 {
		o.Boost = DefaultBoost
	}
	if o.Threshold <= 0 {
		o.Threshold = DefaultThreshold
	}
	o.Common = o.Common.withDefaults(1)
	return o
}
