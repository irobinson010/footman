package audio

import (
	"math"
	"math/rand"
	"testing"
)

func tone(n int, amp float64, off int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(amp * math.Sin(2*math.Pi*300*float64(i+off)/16000))
	}
	return s
}

// run feeds seconds of a sine of the given RMS dBFS in 20 ms frames and
// returns the last second's output RMS and peak.
func run(a *AGC, seconds int, rmsDB float64) (outRMS, outPeak float64) {
	amp := math.Sqrt2 * db(rmsDB)
	off := 0
	for sec := 0; sec < seconds; sec++ {
		for k := 0; k < 50; k++ {
			s := tone(320, amp, off)
			off += 320
			a.Process(s, Speech)
			if sec == seconds-1 {
				outRMS, outPeak = Level(s)
			}
		}
	}
	return
}

func TestAGCQuietReachesTarget(t *testing.T) {
	a := NewAGC(AGCConfig{})
	rms, _ := run(a, 20, -40)
	if math.Abs(rms-DefaultAGCTargetDB) > 1.5 {
		t.Fatalf("rms = %.1f dBFS, want about %.0f", rms, DefaultAGCTargetDB)
	}
}

func TestAGCMaxGain(t *testing.T) {
	a := NewAGC(AGCConfig{MaxGainDB: 10})
	rms, _ := run(a, 20, -40) // wants +20 dB
	if rms > -29 || rms < -31 {
		t.Fatalf("rms = %.1f, want about -30 (capped at +10 dB)", rms)
	}
	if g := a.GainDB(); g > 10.01 {
		t.Fatalf("gain %.1f dB over cap", g)
	}
}

func TestAGCLoudNotClipped(t *testing.T) {
	a := NewAGC(AGCConfig{})
	run(a, 5, -40) // raise the gain first
	// Sudden loud input: full-scale-ish.
	off := 0
	for k := 0; k < 100; k++ {
		s := tone(320, 0.95, off)
		off += 320
		a.Process(s, Speech)
		for _, v := range s {
			if math.Abs(float64(v)) > 0.981 {
				t.Fatalf("frame %d clipped: %v", k, v)
			}
		}
	}
}

func TestAGCNoiseFloorStaysLow(t *testing.T) {
	a := NewAGC(AGCConfig{})
	r := rand.New(rand.NewSource(1))
	var rms float64
	for k := 0; k < 1000; k++ {
		s := make([]float32, 320)
		for i := range s {
			s[i] = float32((r.Float64()*2 - 1) * 0.0017) // about -60 dBFS rms
		}
		a.Process(s, Speech)
		rms, _ = Level(s)
	}
	if rms > -55 || a.Gain() > 1.0001 {
		t.Fatalf("noise pumped up: rms %.1f dBFS, gain %.2f", rms, a.Gain())
	}
}

func TestAGCBurstsAfterSilence(t *testing.T) {
	a := NewAGC(AGCConfig{})
	var last float64
	for burst := 0; burst < 8; burst++ {
		run(a, 1, -42)
		for k := 0; k < 50; k++ { // 1 s silence
			a.Process(make([]float32, 320), Speech)
		}
		last = a.GainDB()
	}
	if last < 10 {
		t.Fatalf("gain %.1f dB after repeated quiet bursts, want it to have risen", last)
	}
}

func TestAGCNoAllocs(t *testing.T) {
	a := NewAGC(AGCConfig{})
	s := tone(320, 0.01, 0)
	if n := testing.AllocsPerRun(100, func() { a.Process(s, Speech) }); n != 0 {
		t.Fatalf("%v allocs", n)
	}
}

func TestAGCStreamCopies(t *testing.T) {
	a := NewAGC(AGCConfig{})
	in := make(chan Frame, 1)
	src := tone(320, 0.01, 0)
	orig := append([]float32(nil), src...)
	in <- Frame{Format: Speech, Samples: src}
	close(in)
	n := 0
	for range a.Stream(t.Context(), in) {
		n++
	}
	if n != 1 {
		t.Fatalf("frames = %d", n)
	}
	for i := range src {
		if src[i] != orig[i] {
			t.Fatal("source frame modified")
		}
	}
}

func TestLevel(t *testing.T) {
	rms, peak := Level(tone(16000, 1, 0))
	if math.Abs(rms+3.01) > 0.05 || math.Abs(peak) > 0.05 {
		t.Fatalf("rms %.2f peak %.2f", rms, peak)
	}
	if r, _ := Level(nil); !math.IsInf(r, -1) {
		t.Fatal("empty should be -Inf")
	}
}
