package audio

import (
	"context"
	"math"
)

// AGC defaults (ADR-0035).
const (
	// DefaultAGCTargetDB is the RMS level the normaliser aims for, in dBFS.
	DefaultAGCTargetDB = -20.0
	// DefaultAGCMaxGainDB caps how much a quiet signal is raised.
	DefaultAGCMaxGainDB = 24.0
	// DefaultAGCGateDB is the frame RMS (dBFS) below which the gain is held
	// rather than raised, so silence and room noise are not pumped up.
	DefaultAGCGateDB = -50.0
)

// AGCConfig tunes an AGC. Zero fields take the defaults.
type AGCConfig struct {
	// TargetDB is the RMS aimed for, in dBFS (default -20).
	TargetDB float64
	// MaxGainDB caps the gain (default +24).
	MaxGainDB float64
	// GateDB is the noise gate threshold in dBFS (default -50).
	GateDB float64
	// Attack and Release are the time constants, in seconds, for lowering
	// and for raising the gain (defaults 0.3 and 2).
	Attack, Release float64
}

// AGC is a slow automatic gain control for speech. It measures each frame's
// RMS and moves one gain, with slow attack and release, toward the value that
// brings the level to the target. Frames below the gate hold the gain, so
// silence is not amplified. A peak guard lowers the gain at once, so the
// output never clips. It is pure Go, works in place and does not allocate.
// An AGC is not safe for concurrent use.
type AGC struct {
	target, gate, maxGain float64
	attack, release       float64
	gain                  float64
}

// NewAGC returns an AGC with unity gain.
func NewAGC(c AGCConfig) *AGC {
	or := func(v, d float64) float64 {
		if v == 0 {
			return d
		}
		return v
	}
	return &AGC{
		target:  db(or(c.TargetDB, DefaultAGCTargetDB)),
		gate:    db(or(c.GateDB, DefaultAGCGateDB)),
		maxGain: db(math.Max(or(c.MaxGainDB, DefaultAGCMaxGainDB), 0)),
		attack:  or(c.Attack, 0.3),
		release: or(c.Release, 2),
		gain:    1,
	}
}

func db(x float64) float64 { return math.Pow(10, x/20) }

// Gain returns the current gain as a factor.
func (a *AGC) Gain() float64 { return a.gain }

// GainDB returns the current gain in dB.
func (a *AGC) GainDB() float64 { return 20 * math.Log10(a.gain) }

// Process normalises samples (interleaved, in format f) in place.
func (a *AGC) Process(samples []float32, f Format) {
	if len(samples) == 0 || !f.Valid() {
		return
	}
	var sum, peak float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
		if v < 0 {
			v = -v
		}
		peak = math.Max(peak, v)
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	dt := float64(len(samples)/f.Channels) / float64(f.SampleRate)

	g0 := a.gain
	g1 := g0
	if rms >= a.gate {
		want := math.Min(a.target/rms, a.maxGain)
		tau := a.release
		if want < g0 {
			tau = a.attack
		}
		g1 = g0 + (want-g0)*(1-math.Exp(-dt/tau))
	}
	// Peak guard: never clip, and stay down afterwards.
	if peak*math.Max(g0, g1) > 0.98 {
		g1 = math.Min(g1, 0.98/peak)
		g0 = math.Min(g0, 0.98/peak)
	}
	a.gain = g1
	if g0 == 1 && g1 == 1 {
		return
	}
	n := float64(len(samples))
	for i, s := range samples {
		g := g0 + (g1-g0)*float64(i)/n
		samples[i] = float32(float64(s) * g)
	}
}

// Stream returns a channel of the frames from in with the AGC applied. The
// frames are copies, so the source's buffers are left alone. The channel
// closes when in does or ctx is done.
func (a *AGC) Stream(ctx context.Context, in <-chan Frame) <-chan Frame {
	out := make(chan Frame, 16)
	go func() {
		defer close(out)
		for fr := range in {
			s := make([]float32, len(fr.Samples))
			copy(s, fr.Samples)
			fr.Samples = s
			a.Process(fr.Samples, fr.Format)
			select {
			case out <- fr:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Level measures samples: the RMS and the peak, both in dBFS (-Inf for
// silence), for level meters.
func Level(samples []float32) (rmsDB, peakDB float64) {
	if len(samples) == 0 {
		return math.Inf(-1), math.Inf(-1)
	}
	var sum, peak float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
		peak = math.Max(peak, math.Abs(v))
	}
	return 20 * math.Log10(math.Sqrt(sum/float64(len(samples)))), 20 * math.Log10(peak)
}
