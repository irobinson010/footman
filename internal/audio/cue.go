package audio

import (
	"math"
	"time"
)

// CueRate is the sample rate of the built-in cues. The player takes a clip in
// any format, so this only needs to be high enough for the pitches used.
const CueRate = 24000

// blip is one soft sine note of a cue.
type blip struct {
	freq float64       // Hz
	dur  time.Duration // including the fades
	gain float64       // peak amplitude, 0 to 1
}

// cueFade is the length of the fade in and out of each note, which keeps
// the start and end from clicking.
const cueFade = 10 * time.Millisecond

// cueGap is the silence between two notes of a cue.
const cueGap = 12 * time.Millisecond

// WakeEarcon returns the short sound played when the wake word is heard: two
// soft rising notes, 150 ms in all and quiet (peak 0.12, about -18 dBFS), so
// that it says "I heard you" without startling anyone or drowning the
// command that follows. It is generated, so there is no sound file to ship
// or license.
func WakeEarcon(rate int) Clip {
	return cue(rate, []blip{
		{freq: 880, dur: 70 * time.Millisecond, gain: 0.12},
		{freq: 1318.5, dur: 68 * time.Millisecond, gain: 0.12},
	})
}

// MissCue returns the sound played when a heard wake word was not confirmed
// by speech-to-text: two softer, lower falling notes, 150 ms in all (peak
// 0.07), the opposite of WakeEarcon, so that the user knows to say it again.
func MissCue(rate int) Clip {
	return cue(rate, []blip{
		{freq: 392, dur: 70 * time.Millisecond, gain: 0.07},
		{freq: 294, dur: 68 * time.Millisecond, gain: 0.07},
	})
}

// cue renders notes one after another, with cueGap of silence between them,
// as mono float samples at rate (CueRate if it is not positive).
func cue(rate int, notes []blip) Clip {
	if rate <= 0 {
		rate = CueRate
	}
	f := Format{SampleRate: rate, Channels: 1}
	var out []float32
	for i, n := range notes {
		if i > 0 {
			out = append(out, make([]float32, f.Samples(cueGap))...)
		}
		total := f.Samples(n.dur)
		fade := f.Samples(cueFade)
		for j := range total {
			// Raised-cosine fade in and out.
			env := 1.0
			if j < fade {
				env = 0.5 * (1 - math.Cos(math.Pi*float64(j)/float64(fade)))
			} else if k := total - 1 - j; k < fade {
				env = 0.5 * (1 - math.Cos(math.Pi*float64(k)/float64(fade)))
			}
			out = append(out, float32(n.gain*env*math.Sin(2*math.Pi*n.freq*float64(j)/float64(rate))))
		}
	}
	return Clip{Format: f, Samples: out}
}
