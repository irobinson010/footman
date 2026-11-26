package speech

import (
	"context"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
)

// Frame is a short chunk of microphone audio. It is an alias of audio.Frame so
// the platform layer and the speech engines share one type.
type Frame = audio.Frame

// Audio is a complete utterance held in memory. It is an alias of audio.Clip.
type Audio = audio.Clip

// AudioStream is synthesized speech delivered incrementally. It is an alias of
// audio.Stream.
type AudioStream = audio.Stream

// Segment is one stretch of speech found by a VAD, from where speech started
// to where it ended.
type Segment struct {
	// Start and End are offsets from the start of the input stream.
	Start, End time.Duration
	// Audio holds the speech in the segment.
	Audio Audio
}

// WakeKind says what a WakeEvent reports.
type WakeKind string

const (
	// WakeKindWake is a wake word (the zero value of WakeEvent.Kind is
	// treated the same).
	WakeKindWake WakeKind = "wake"
	// WakeKindBreakGlass is the break-glass stop phrase ("stop everything",
	// "cancel everything") spoken right after a wake word (ADR-0017). A
	// spotter never reports it without the wake word.
	WakeKindBreakGlass WakeKind = "break-glass"
)

// WakeEvent reports that a wake word was heard.
type WakeEvent struct {
	// Kind is WakeKindWake (or empty) for a wake word, WakeKindBreakGlass
	// for the stop phrase that followed one.
	Kind WakeKind
	// Keyword is the wake word that matched, such as "ashford"; for a
	// break-glass event, the phrase, such as "stop everything".
	Keyword string
	// At is the offset from the start of the input stream where it ended.
	At time.Duration
	// Score is the engine's confidence in [0, 1], or 0 if the engine does
	// not report one.
	Score float32
}

// Word is one word of a transcript with its timing.
type Word struct {
	// Text is the word as recognized.
	Text string
	// Start and End are offsets from the start of the audio.
	Start, End time.Duration
}

// Transcript is the result of speech-to-text.
type Transcript struct {
	// Text is the full recognized text.
	Text string
	// Language is the BCP 47 code of the detected or configured language.
	Language string
	// Confidence is the engine's overall confidence in [0, 1], or 0 if the
	// engine does not report one.
	Confidence float32
	// Words holds per-word timings when the engine provides them.
	Words []Word
}

// Voice selects how text-to-speech sounds. A persona pack's `[voice]`
// section maps onto it: `model` is ID, `speaker` is Speaker and `speed` is
// Speed (ADR-0037).
type Voice struct {
	// ID is the voice model: a models-catalog ID (such as
	// "kokoro-en-int8"), or an absolute path to a model file or folder (a
	// persona pack's `voice/…` path, resolved against the pack folder by
	// the caller). Empty means the engine's default model.
	ID string
	// Speaker picks one speaker of a multi-speaker model, by name (such as
	// "bm_george") or by index ("26"). Empty means the model's default
	// speaker; a single-speaker model (such as a fine-tuned persona voice)
	// ignores it.
	Speaker string
	// Language is the BCP 47 code to speak in, such as "en-GB"; empty means
	// the voice default. Engines that know accents use it to pick one.
	Language string
	// Speed scales the speaking rate; 0 means the voice default (1.0).
	Speed float32
}

// VAD (voice activity detection) finds the stretches of speech in a stream of
// frames.
type VAD interface {
	// Detect reads frames until the channel closes or ctx is done, and sends
	// each completed speech segment on the returned channel, which is closed
	// when Detect stops. Speech still open when frames closes is flushed as a
	// final segment.
	Detect(ctx context.Context, frames <-chan Frame) <-chan Segment
}

// WakeWord spots wake words in a stream of frames.
type WakeWord interface {
	// Listen reads frames until the channel closes or ctx is done, and sends a
	// WakeEvent each time a wake word is heard. The returned channel is closed
	// when Listen stops.
	Listen(ctx context.Context, frames <-chan Frame) <-chan WakeEvent
}

// STT turns speech into text.
type STT interface {
	// Transcribe returns the text spoken in a.
	Transcribe(ctx context.Context, a Audio) (Transcript, error)
}

// TTS turns text into speech.
type TTS interface {
	// Speak starts synthesizing text in voice v and returns the audio as it is
	// produced, so playback can start before synthesis finishes. Cancelling
	// ctx stops synthesis; the caller must Close the stream.
	Speak(ctx context.Context, text string, v Voice) (AudioStream, error)
}

// Registries of speech engines, keyed by the engine name used in config
// (for example `[speech] stt = { engine = "whispercpp" }`).
var (
	// VADs holds voice activity detectors.
	VADs = registry.New[VAD]("vad")
	// WakeWords holds wake-word spotters.
	WakeWords = registry.New[WakeWord]("wake word")
	// STTs holds speech-to-text engines.
	STTs = registry.New[STT]("stt")
	// TTSs holds text-to-speech engines.
	TTSs = registry.New[TTS]("tts")
)
