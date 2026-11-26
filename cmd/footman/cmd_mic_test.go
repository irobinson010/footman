package main

import (
	"bufio"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/platform/fake"
)

// stubAudio lists several sources and plays a sine of a set level for each;
// it never touches a device.
type stubAudio struct {
	devs   []platform.Device
	level  map[string]float64 // peak amplitude by source ID
	opened []string
}

func (s *stubAudio) Inputs(context.Context) ([]platform.Device, error)  { return s.devs, nil }
func (s *stubAudio) Outputs(context.Context) ([]platform.Device, error) { return nil, nil }
func (s *stubAudio) Play(context.Context, string, audio.Stream) error   { return nil }
func (s *stubAudio) Capture(_ context.Context, id string, f audio.Format) (<-chan audio.Frame, error) {
	s.opened = append(s.opened, id)
	if id == "" {
		id = s.devs[0].ID
	}
	ch := make(chan audio.Frame)
	go func() {
		defer close(ch)
		for i := 0; i < 100; i++ { // 2 s
			smp := make([]float32, 320)
			for k := range smp {
				smp[k] = float32(s.level[id] * math.Sin(2*math.Pi*300*float64(i*320+k)/16000))
			}
			ch <- audio.Frame{Format: f, Samples: smp, Offset: time.Duration(i) * 20 * time.Millisecond}
		}
	}()
	return ch, nil
}

func newStub() *stubAudio {
	return &stubAudio{
		devs: []platform.Device{
			{ID: "alsa_input.mic", Name: "Built-in mic", Default: true},
			{ID: "echo-cancel-source", Name: "Echo-Cancel Source"},
		},
		level: map[string]float64{"alsa_input.mic": 0.003, "echo-cancel-source": 0.2},
	}
}

func micOpts(a platform.Audio, in string, ask bool, out *strings.Builder) micOptions {
	return micOptions{
		Audio: a, ConfigPath: filepath.Join(os.TempDir(), "unused"), Duration: 5 * time.Second,
		Ask: ask, In: bufio.NewReader(strings.NewReader(in)), Out: out,
	}
}

func TestMicCheckWarnsWhenQuiet(t *testing.T) {
	var out strings.Builder
	o := micOpts(newStub(), "", false, &out)
	o.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	if err := micCheck(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Built-in mic", "echo-cancelled", "too low", "Echo-Cancel Source", "input volume", "rms"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestMicCheckPicksSourceAndSaves(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("# my comment\nversion = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	a := newStub()
	o := micOpts(a, "2\ny\n\n", true, &out) // try source 2, save it, finish
	o.ConfigPath = cfg
	if err := micCheck(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), `microphone = "echo-cancel-source"`) || !strings.Contains(string(b), "# my comment") {
		t.Fatalf("config:\n%s", b)
	}
	if !strings.Contains(out.String(), "OK: the level is good") {
		t.Errorf("no OK for the loud source:\n%s", out.String())
	}
}

func TestMicCheckDeclineSaveLeavesConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	var out strings.Builder
	o := micOpts(newStub(), "2\nn\n\n", true, &out)
	o.ConfigPath = cfg
	if err := micCheck(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("config written without consent")
	}
}

func TestMicCheckCommandUsesFakePlatform(t *testing.T) {
	installEnv(t, "")
	code, out, errs := runCmd("mic", "check", "--seconds", "1")
	if code != 0 {
		t.Fatalf("mic check = %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, "Fake microphone") || !strings.Contains(out, "No audio arrived") {
		t.Fatalf("output:\n%s", out)
	}
	if code, _, _ := runCmd("mic"); code != 2 {
		t.Fatalf("bare mic = %d, want 2", code)
	}
}

func TestMicCheckCommandWithQueuedAudio(t *testing.T) {
	_, p := installEnv(t, "")
	p.Audio().(*fake.Audio).QueueCapture(audio.Frame{Format: audio.Speech, Samples: make([]float32, 16000)})
	code, out, _ := runCmd("mic", "check", "--seconds", "1")
	if code != 0 || !strings.Contains(out, "silence") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}

func TestEchoCancelSource(t *testing.T) {
	devs := []platform.Device{
		{ID: "alsa_input.mic", Name: "Mic"},
		{ID: "echo-cancel-source.monitor", Name: "Monitor of Echo-Cancel Source"},
		{ID: "echo-cancel-source", Name: "Echo-Cancel Source"},
	}
	d, ok := echoCancelSource(devs)
	if !ok || d.ID != "echo-cancel-source" {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := echoCancelSource(devs[:1]); ok {
		t.Fatal("found one where none")
	}
}
