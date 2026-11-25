//go:build linux

package linux

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/platform"
)

func fakePactl(sources, sinks, defSource, defSink string) runFunc {
	return func(_ context.Context, s runSpec) (runResult, error) {
		switch strings.Join(s.Args, " ") {
		case "--format=json list sources":
			return runResult{Stdout: sources}, nil
		case "--format=json list sinks":
			return runResult{Stdout: sinks}, nil
		case "get-default-source":
			return runResult{Stdout: defSource + "\n"}, nil
		case "get-default-sink":
			return runResult{Stdout: defSink + "\n"}, nil
		}
		return runResult{}, errors.New("unexpected " + strings.Join(s.Args, " "))
	}
}

func TestAudioListing(t *testing.T) {
	a := newAudio(fakePactl(
		`[{"name":"mic1","description":"Mic"},{"name":"spk.monitor","description":"Monitor of Spk"},{"name":"mic2","description":"Webcam"}]`,
		`[{"name":"spk","description":"Speakers"}]`, "mic2", "spk"))
	in, err := a.Inputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []platform.Device{{ID: "mic1", Name: "Mic"}, {ID: "mic2", Name: "Webcam", Default: true}}
	if !slices.Equal(in, want) {
		t.Errorf("Inputs = %+v, want %+v", in, want)
	}
	out, err := a.Outputs(context.Background())
	if err != nil || len(out) != 1 || !out[0].Default || out[0].ID != "spk" {
		t.Errorf("Outputs = %+v, %v", out, err)
	}
}

func TestAudioMissingTools(t *testing.T) {
	a := newAudio(func(context.Context, runSpec) (runResult, error) { return runResult{}, errNoTool })
	if _, err := a.Inputs(context.Background()); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Inputs = %v, want ErrUnsupported", err)
	}
	a = newAudio(fakePactl(`[]`, `[]`, "", ""))
	a.parec, a.pacat, a.pactl = "/nonexistent/parec", "/nonexistent/pacat", "/nonexistent/pactl"
	if _, err := a.Capture(context.Background(), "", audio.Speech); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Capture = %v, want ErrUnsupported", err)
	}
	s := audio.NewClipStream(audio.Clip{Format: audio.Speech, Samples: make([]float32, 10)}, 5)
	if err := a.Play(context.Background(), "", s); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Play = %v, want ErrUnsupported", err)
	}
	if _, err := a.Watch(context.Background()); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("Watch = %v, want ErrUnsupported", err)
	}
}

func TestAudioUnknownDevice(t *testing.T) {
	a := newAudio(fakePactl(`[{"name":"mic1"}]`, `[{"name":"spk"}]`, "", ""))
	if _, err := a.Capture(context.Background(), "nope", audio.Speech); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("Capture = %v, want ErrNotFound", err)
	}
	s := audio.NewClipStream(audio.Clip{Format: audio.Speech}, 5)
	if err := a.Play(context.Background(), "nope", s); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("Play = %v, want ErrNotFound", err)
	}
	if _, err := s.Recv(); !errors.Is(err, audio.ErrClosed) {
		t.Errorf("stream not closed by Play: %v", err)
	}
}

func f32le(v ...float32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}
	return b
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAudioCaptureFake(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	// 5 samples plus 2 stray bytes: the partial sample must be dropped, not
	// delivered, and the recorder keeps running until ctx is done.
	if err := os.WriteFile(data, append(f32le(0.5, -0.5, 0.25, 1, -1), 1, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args")
	a := newAudio(fakePactl(`[{"name":"mic1"}]`, `[]`, "", ""))
	a.parec = writeScript(t, dir, "parec", `echo "$@" > `+argsFile+"\ncat "+data+"\nexec sleep 30\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := a.Capture(ctx, "mic1", audio.Speech)
	if err != nil {
		t.Fatal(err)
	}
	var got []float32
	for len(got) < 5 {
		select {
		case fr := <-ch:
			if fr.Format != audio.Speech {
				t.Fatalf("format = %+v", fr.Format)
			}
			if want := audio.Speech.Duration(len(got)); fr.Offset != want {
				t.Errorf("offset = %v, want %v", fr.Offset, want)
			}
			got = append(got, fr.Samples...)
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout; got %v", got)
		}
	}
	if !slices.Equal(got, []float32{0.5, -0.5, 0.25, 1, -1}) {
		t.Errorf("samples = %v", got)
	}
	if a.Capturing() != 1 {
		t.Errorf("Capturing = %d, want 1", a.Capturing())
	}
	args, _ := os.ReadFile(argsFile)
	for _, w := range []string{"--format=float32le", "--rate=16000", "--channels=1", "--device=mic1", "--raw"} {
		if !strings.Contains(string(args), w) {
			t.Errorf("parec args %q missing %q", args, w)
		}
	}

	cancel() // clean shutdown: channel closes, recorder gone, mic count back to 0
	deadline := time.After(5 * time.Second)
drain:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break drain
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel")
		}
	}
	for i := 0; a.Capturing() != 0; i++ {
		if i > 100 {
			t.Fatalf("Capturing = %d after close", a.Capturing())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAudioPlayFake(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	a := newAudio(fakePactl(`[]`, `[{"name":"spk"}]`, "", ""))
	a.pacat = writeScript(t, dir, "pacat", "cat > "+out+"\n")
	clip := audio.Clip{Format: audio.Speech, Samples: []float32{0.1, 0.2, 0.3, 0.4, 0.5}}
	if err := a.Play(context.Background(), "spk", audio.NewClipStream(clip, 2)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if !slices.Equal(got, f32le(clip.Samples...)) {
		t.Errorf("pacat got %v", got)
	}
}

func TestAudioPlayCancel(t *testing.T) {
	dir := t.TempDir()
	a := newAudio(fakePactl(`[]`, `[]`, "", ""))
	a.pacat = writeScript(t, dir, "pacat", "exec sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := a.Play(ctx, "", slowStream{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Play = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("Play took %v to stop", d)
	}
}

// slowStream yields silence forever, 10 ms at a time.
type slowStream struct{}

func (slowStream) Format() audio.Format { return audio.Speech }
func (slowStream) Recv() (audio.Frame, error) {
	time.Sleep(10 * time.Millisecond)
	return audio.Frame{Format: audio.Speech, Samples: make([]float32, 160)}, nil
}
func (slowStream) Close() error { return nil }

func TestAudioWatchFake(t *testing.T) {
	dir := t.TempDir()
	a := newAudio(nil)
	a.pactl = writeScript(t, dir, "pactl", `cat <<'EOF'
Event 'change' on sink #3
Event 'new' on source #52
Event 'remove' on sink #9
Event 'change' on server
Event 'new' on sink-input #7
EOF
`)
	ch, err := a.Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []DeviceChange
	for c := range ch {
		got = append(got, c)
	}
	want := []DeviceChange{{"source", "new"}, {"sink", "remove"}, {"server", "change"}}
	if !slices.Equal(got, want) {
		t.Errorf("changes = %+v, want %+v", got, want)
	}
}
