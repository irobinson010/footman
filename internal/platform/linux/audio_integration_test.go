//go:build linux && integration

package linux

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
)

// These tests need a PulseAudio or PipeWire server (pipewire-pulse) with
// pactl on the PATH, and skip without one. They NEVER open a real microphone
// or play on a real speaker: every test creates its own null sink, plays into
// it and records its .monitor, then unloads it (also on failure).

// virtualSink creates a null sink and returns its name.
func virtualSink(t *testing.T) string {
	t.Helper()
	if err := exec.Command("pactl", "info").Run(); err != nil {
		t.Skipf("no PulseAudio/PipeWire server: %v", err)
	}
	name := fmt.Sprintf("footman_test_%d_%d", os.Getpid(), time.Now().UnixNano()%100000)
	out, err := exec.Command("pactl", "load-module", "module-null-sink", "sink_name="+name,
		"sink_properties=device.description="+name).Output()
	if err != nil {
		t.Skipf("cannot create a virtual sink: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("pactl", "unload-module", id).Run() })
	return name
}

func tone(hz float64, d time.Duration, amp float32) []float32 {
	n := int(d.Seconds() * 16000)
	s := make([]float32, n)
	for i := range s {
		s[i] = amp * float32(math.Sin(2*math.Pi*hz*float64(i)/16000))
	}
	return s
}

func peak(s []float32) float32 {
	var p float32
	for _, v := range s {
		p = max(p, float32(math.Abs(float64(v))))
	}
	return p
}

func TestRealAudioDevicesAndWatch(t *testing.T) {
	if err := exec.Command("pactl", "info").Run(); err != nil {
		t.Skipf("no PulseAudio/PipeWire server: %v", err)
	}
	a := newAudio(defaultRun)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wctx, wcancel := context.WithCancel(ctx)
	changes, err := a.Watch(wctx)
	if err != nil {
		t.Fatal(err)
	}
	name := virtualSink(t)

	sawNew := false
	timeout := time.After(5 * time.Second)
	for !sawNew {
		select {
		case c := <-changes:
			sawNew = c == DeviceChange{Kind: "sink", Action: "new"}
		case <-timeout:
			t.Fatal("no new-sink change reported")
		}
	}
	wcancel()
	for range changes { // closes after cancel
	}

	outs, err := a.Outputs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range outs {
		found = found || d.ID == name
	}
	if !found {
		t.Errorf("virtual sink %q not in Outputs %+v", name, outs)
	}
	ins, err := a.Inputs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ins {
		if strings.HasSuffix(d.ID, ".monitor") {
			t.Errorf("monitor %q listed as a microphone", d.ID)
		}
	}
}

// TestRealAudioRoundTrip plays a known tone into a virtual sink and records
// its monitor: playback is verified through the monitor, and the captured
// frames are 16 kHz mono float32.
func TestRealAudioRoundTrip(t *testing.T) {
	sink := virtualSink(t)
	a := newAudio(defaultRun)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	frames, err := a.Capture(cctx, sink+".monitor", audio.Speech)
	if err != nil {
		t.Fatal(err)
	}
	if a.Capturing() != 1 {
		t.Errorf("Capturing = %d, want 1", a.Capturing())
	}

	const hz = 440
	clip := audio.Clip{Format: audio.Speech, Samples: tone(hz, time.Second, 0.5)}
	done := make(chan error, 1)
	go func() { done <- a.Play(ctx, sink, audio.NewClipStream(clip, 320)) }()

	var got []float32
	var maxGap time.Duration
	last := time.Now()
	playing := true
	var tail int
	for playing || tail < 8000 { // keep 0.5 s after Play returns
		select {
		case fr, ok := <-frames:
			if !ok {
				t.Fatal("capture closed early")
			}
			if fr.Format != audio.Speech {
				t.Fatalf("frame format %+v", fr.Format)
			}
			if now := time.Now(); now.Sub(last) > maxGap {
				maxGap = now.Sub(last)
			}
			last = time.Now()
			got = append(got, fr.Samples...)
			if !playing {
				tail += len(fr.Samples)
			}
		case err := <-done:
			if err != nil {
				t.Fatalf("Play: %v", err)
			}
			playing = false
			done = nil
		}
	}

	// Find the loud region and check its length and pitch.
	first, lastLoud := -1, -1
	for i, v := range got {
		if math.Abs(float64(v)) > 0.1 {
			if first < 0 {
				first = i
			}
			lastLoud = i
		}
	}
	if first < 0 {
		t.Fatal("recorded only silence")
	}
	loud := got[first : lastLoud+1]
	if d := audio.Speech.Duration(len(loud)); d < 900*time.Millisecond || d > 1100*time.Millisecond {
		t.Errorf("recorded tone lasts %v, want ~1s", d)
	}
	if p := peak(loud); p < 0.4 || p > 0.6 {
		t.Errorf("peak = %v, want ~0.5", p)
	}
	crossings := 0
	for i := 1; i < len(loud); i++ {
		if (loud[i-1] < 0) != (loud[i] < 0) {
			crossings++
		}
	}
	secs := audio.Speech.Duration(len(loud)).Seconds()
	if hzGot := float64(crossings) / 2 / secs; math.Abs(hzGot-hz) > 15 {
		t.Errorf("pitch = %.0f Hz, want %d", hzGot, hz)
	}
	t.Logf("recorded %d samples; largest gap between frames %v", len(got), maxGap)

	ccancel()
	deadline := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-frames:
		case <-deadline:
			t.Fatal("capture channel not closed after cancel")
		}
	}
	if n := a.Capturing(); n != 0 {
		t.Errorf("Capturing = %d after close", n)
	}
}

// clickStream yields silence in real time, 10 ms per frame, then one frame of
// loud tone, noting when it handed that frame over.
type clickStream struct {
	n       int
	clickAt time.Time
}

func (s *clickStream) Format() audio.Format { return audio.Speech }
func (s *clickStream) Close() error         { return nil }
func (s *clickStream) Recv() (audio.Frame, error) {
	s.n++
	switch {
	case s.n <= 60:
		time.Sleep(10 * time.Millisecond)
		return audio.Frame{Format: audio.Speech, Samples: make([]float32, 160)}, nil
	case s.n == 61:
		s.clickAt = time.Now()
		return audio.Frame{Format: audio.Speech, Samples: tone(1000, 10*time.Millisecond, 0.8)}, nil
	case s.n <= 100:
		time.Sleep(10 * time.Millisecond)
		return audio.Frame{Format: audio.Speech, Samples: make([]float32, 160)}, nil
	}
	return audio.Frame{}, os.ErrClosed
}

// TestRealAudioLatency measures how long a sound takes to get from the
// producer (the Stream handed to Play) through PipeWire to a Capture frame.
// That covers both directions, so capture alone is faster than reported.
func TestRealAudioLatency(t *testing.T) {
	sink := virtualSink(t)
	a := newAudio(defaultRun)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var results []time.Duration
	for range 5 {
		frames, err := a.Capture(ctx, sink+".monitor", audio.Speech)
		if err != nil {
			t.Fatal(err)
		}
		cs := &clickStream{}
		pctx, pcancel := context.WithCancel(ctx)
		go func() { _ = a.Play(pctx, sink, cs) }()
		var d time.Duration
		timeout := time.After(5 * time.Second)
	wait:
		for {
			select {
			case fr := <-frames:
				if !cs.clickAt.IsZero() && peak(fr.Samples) > 0.2 {
					d = time.Since(cs.clickAt)
					break wait
				}
			case <-timeout:
				t.Fatal("click never arrived")
			}
		}
		pcancel()
		results = append(results, d)
		time.Sleep(200 * time.Millisecond)
	}
	slices.Sort(results)
	t.Logf("producer -> Play -> null sink -> monitor -> Capture frame: min %v median %v max %v",
		results[0], results[len(results)/2], results[len(results)-1])
	if m := results[len(results)/2]; m > 50*time.Millisecond {
		t.Errorf("median latency %v exceeds the 50 ms target", m)
	}
}
