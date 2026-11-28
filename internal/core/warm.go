package core

import "context"

// A Warmer is a part (a text-to-speech engine or an AI provider) that can get
// ready for a request that is about to come: load a model that was unloaded
// while idle, open a connection, look up a key. Warm never fails and never
// blocks the caller for long; the request itself reports any problem.
type Warmer interface{ Warm(ctx context.Context) }

// warmUp starts getting the reply path ready as soon as a command is
// expected (the wake word was heard, or the command hotkey went down), so
// the work overlaps the time the user spends speaking instead of following
// it (ADR-0051). A speech engine that was unloaded for being idle takes
// about a second to load again, and the first request to the AI pays for
// the keyring and the TLS handshake; both used to come after the end of
// speech. The speech engine is warmed only if a reply would be spoken.
func (p *Pipeline) warmUp() {
	if p.d.TTS != nil && p.shouldSpeak(false) {
		if w, ok := p.d.TTS.(Warmer); ok {
			go w.Warm(p.ctx)
		}
	}
	if p.d.LLM != nil {
		if w, ok := p.d.LLM.(Warmer); ok {
			go w.Warm(p.ctx)
		}
	}
}
