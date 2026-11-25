package idle

import (
	"context"
	"sync"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/speech"
)

// STT makes a Holder of a speech-to-text engine usable as one. Each
// Transcribe holds the engine for its duration, loading it first if needed.
type STT struct{ H *Holder[speech.STT] }

// Transcribe implements speech.STT.
func (s STT) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	v, release, err := s.H.Acquire()
	if err != nil {
		return speech.Transcript{}, err
	}
	defer release()
	return v.Transcribe(ctx, a)
}

// Hold keeps the engine loaded until release is called, loading it in the
// background if it was unloaded; it never blocks. The pipeline holds it from
// the moment a push-to-talk key goes down until the utterance has been
// transcribed, so a reload overlaps the speech rather than following it, and
// the engine cannot be unloaded while the user is still speaking.
func (s STT) Hold() (release func()) {
	got := make(chan func(), 1)
	go func() {
		_, rel, err := s.H.Acquire()
		if err != nil {
			rel = func() {}
		}
		got <- rel
	}()
	var once sync.Once
	return func() { once.Do(func() { go func() { (<-got)() }() }) }
}

// Close closes the engine for good.
func (s STT) Close() error { return s.H.Close() }

// VAD makes a Holder of a VAD usable as one. The engine is held for as long as
// a Detect stream is open, so while the microphone is being listened to it
// stays loaded; it unloads once listening stops and the idle time passes.
type VAD struct{ H *Holder[speech.VAD] }

// Detect implements speech.VAD.
func (v VAD) Detect(ctx context.Context, frames <-chan speech.Frame) <-chan speech.Segment {
	out := make(chan speech.Segment)
	inner, release, err := v.H.Acquire()
	if err != nil {
		close(out)
		return out
	}
	in := inner.Detect(ctx, frames)
	go func() {
		defer release()
		defer close(out)
		for seg := range in {
			select {
			case out <- seg:
			case <-ctx.Done():
				for range in { // let the engine finish
				}
				return
			}
		}
	}()
	return out
}

// Close closes the engine for good.
func (v VAD) Close() error { return v.H.Close() }

// TTS makes a Holder of a text-to-speech engine usable as one. The engine is
// held until the returned stream is closed.
type TTS struct{ H *Holder[speech.TTS] }

// Speak implements speech.TTS.
func (t TTS) Speak(ctx context.Context, text string, voice speech.Voice) (speech.AudioStream, error) {
	inner, release, err := t.H.Acquire()
	if err != nil {
		return nil, err
	}
	s, err := inner.Speak(ctx, text, voice)
	if err != nil {
		release()
		return nil, err
	}
	return &leased{Stream: s, release: release}, nil
}

// Close closes the engine for good.
func (t TTS) Close() error { return t.H.Close() }

type leased struct {
	audio.Stream
	once    sync.Once
	release func()
}

func (l *leased) Close() error {
	err := l.Stream.Close()
	l.once.Do(l.release)
	return err
}
