package core

import (
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
)

// rawRing keeps the last few seconds of the microphone stream, as the
// wake-word spotter and the VAD see it. The second wake stage (ADR-0041)
// takes the audio around a wake word from it, so the check does not depend
// on the VAD having put the wake word in a segment: a short word, or one
// whose onset the headset's noise gate or the VAD clipped, may be in none.
//
// It is written by the capture fan-out and read by the pipeline, so it has
// its own lock. It lives in memory only, is never logged, and is dropped
// when the microphone session ends. The buffer is allocated once; writing a
// frame only copies it.
type rawRing struct {
	mu  sync.Mutex
	buf []float32 // circular; len is the capacity
	end int       // stream sample index one past the newest sample held
	n   int       // samples held, at most len(buf)
}

// rawRingLen is how much of the stream is kept: enough for wakeRawHead
// before a wake word, and the frames that arrive before the pipeline takes
// its snapshot.
const rawRingLen = 4 * time.Second

func newRawRing(d time.Duration) *rawRing {
	return &rawRing{buf: make([]float32, audio.Speech.Samples(d))}
}

// write adds a frame. A frame that does not follow the one before (a gap in
// the stream) starts the ring afresh, so it never joins unrelated audio.
func (r *rawRing) write(f audio.Frame) {
	samples := f.Samples
	if f.Format != audio.Speech {
		samples = audio.ToSpeech(audio.Clip{Format: f.Format, Samples: f.Samples}).Samples
	}
	start := audio.Speech.Samples(f.Offset)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n > 0 && start != r.end {
		r.n = 0
	}
	if len(samples) > len(r.buf) { // only the newest fit
		start += len(samples) - len(r.buf)
		samples = samples[len(samples)-len(r.buf):]
	}
	pos := start % len(r.buf)
	c := copy(r.buf[pos:], samples)
	copy(r.buf, samples[c:])
	r.end = start + len(samples)
	r.n = min(r.n+len(samples), len(r.buf))
}

// slice returns a copy of the stream from stream offset from to to, cut to
// what the ring still holds, and the offset it starts at. It returns nil if
// none of that is held.
func (r *rawRing) slice(from, to time.Duration) ([]float32, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lo := max(audio.Speech.Samples(from), r.end-r.n, 0)
	hi := min(audio.Speech.Samples(to), r.end)
	if hi <= lo {
		return nil, 0
	}
	out := make([]float32, hi-lo)
	pos := lo % len(r.buf)
	c := copy(out, r.buf[pos:])
	copy(out[c:], r.buf)
	return out, audio.Speech.Duration(lo)
}
