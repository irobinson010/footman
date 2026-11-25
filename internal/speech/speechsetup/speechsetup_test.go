package speechsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/models"
)

// A machine with a Vulkan GPU and both native engines.
func gpuHW() accel.Hardware {
	return accel.Hardware{RAM: 32 << 30, CPUs: 16, VulkanLoader: true,
		GPUs: []accel.GPU{{Vendor: "amd", Device: "0x164e", Integrated: true, Vulkan: true}}}
}

func engines() map[string][]string {
	return map[string][]string{
		accel.EngineWhisperCpp:     {accel.CPU, accel.Vulkan},
		accel.EngineSherpaParakeet: {accel.CPU},
	}
}

// fakeClock only moves when an engine "takes time".
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) spend(d time.Duration) { c.now = c.now.Add(d) }

type fakeSTT struct {
	c      *fakeClock
	each   time.Duration
	err    error
	calls  int
	closed *int
}

func (f *fakeSTT) Transcribe(context.Context, speech.Audio) (speech.Transcript, error) {
	f.calls++
	if f.err != nil {
		return speech.Transcript{}, f.err
	}
	f.c.spend(f.each)
	return speech.Transcript{Text: "x"}, nil
}

func (f *fakeSTT) Close() error { *f.closed++; return nil }

type rig struct {
	t       *testing.T
	env     Env
	clock   *fakeClock
	cfg     string
	steps   []string
	fetched []string
	opened  []accel.Choice
	closed  int
	// per accelerator: time per transcription, load time, open error.
	each    map[string]time.Duration
	load    map[string]time.Duration
	openErr map[string]error
	runErr  map[string]error
	records []string
}

func newRig(t *testing.T, config string) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, clock: &fakeClock{now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)},
		each: map[string]time.Duration{}, load: map[string]time.Duration{},
		openErr: map[string]error{}, runErr: map[string]error{}}
	r.cfg = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(r.cfg, []byte("version = 1\n"+config), 0o600); err != nil {
		t.Fatal(err)
	}
	r.env = Env{
		ConfigPath: r.cfg, ModelsDir: filepath.Join(dir, "models"),
		Hardware: gpuHW(), Engines: engines(),
		Step: func(f string, a ...any) { r.steps = append(r.steps, fmt.Sprintf(f, a...)) },
		Record: func(path, id string, isDir bool) error {
			r.records = append(r.records, fmt.Sprintf("%s|%s|%v", filepath.Base(path), id, isDir))
			return nil
		},
		Fetch: func(_ context.Context, m models.Model, dir string, o models.FetchOptions) (string, error) {
			r.fetched = append(r.fetched, m.ID)
			if o.Record != nil {
				_ = o.Record(dir, true)
				_ = o.Record(m.Path(dir), false)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			if m.Archive != "" {
				return m.Path(dir), os.MkdirAll(m.Path(dir), 0o755)
			}
			f, err := os.Create(m.Path(dir))
			if err != nil {
				return "", err
			}
			defer f.Close()
			return m.Path(dir), f.Truncate(m.Size)
		},
		Open: func(_ context.Context, c accel.Choice) (speech.STT, error) {
			r.opened = append(r.opened, c)
			if err := r.openErr[c.Accelerator]; err != nil {
				return nil, err
			}
			r.clock.spend(r.load[c.Accelerator])
			return &fakeSTT{c: r.clock, each: r.each[c.Accelerator], err: r.runErr[c.Accelerator], closed: &r.closed}, nil
		},
		Now: r.clock.Now, Budget: time.Second, MaxRuns: 3,
	}
	return r
}

func (r *rig) cfgText() string { b, _ := os.ReadFile(r.cfg); return string(b) }
func (r *rig) log() string     { return strings.Join(r.steps, "\n") }

func TestFastGPUKeepsAuto(t *testing.T) {
	r := newRig(t, "")
	r.load[accel.Vulkan], r.each[accel.Vulkan] = 29*time.Second, 150*time.Millisecond
	r.each[accel.CPU] = 600 * time.Millisecond
	res, err := Run(context.Background(), r.env)
	if err != nil {
		t.Fatal(err)
	}
	if res.PickedCPU || res.Choice.Accelerator != accel.Vulkan {
		t.Fatalf("result = %+v\n%s", res, r.log())
	}
	if got := r.cfgText(); strings.Contains(got, "accelerator") {
		t.Fatalf("config changed: %q", got)
	}
	if want := []string{"whisper-turbo-q5", "silero-vad", "parakeet-tdt-0.6b-v2"}; strings.Join(r.fetched, ",") != strings.Join(want, ",") {
		t.Fatalf("fetched %v, want %v", r.fetched, want)
	}
	if res.GPU.Median != 150*time.Millisecond || res.CPU.Median != 600*time.Millisecond {
		t.Fatalf("timings gpu=%+v cpu=%+v", res.GPU, res.CPU)
	}
	if res.GPU.Load < 29*time.Second {
		t.Fatalf("warm-up not measured: %v", res.GPU.Load)
	}
	if r.closed != 2 {
		t.Fatalf("closed %d engines, want 2", r.closed)
	}
	for _, want := range []string{"warming up vulkan", "GPU: loaded", "CPU: loaded", "using vulkan"} {
		if !strings.Contains(r.log(), want) {
			t.Errorf("progress lacks %q:\n%s", want, r.log())
		}
	}
}

func TestSlowGPUPicksCPUAndSaysWhy(t *testing.T) {
	r := newRig(t, "# my notes\n[speech.stt]\nengine = \"auto\"\n")
	r.load[accel.Vulkan], r.each[accel.Vulkan] = 20*time.Second, 2*time.Second // a 2-CU iGPU
	r.each[accel.CPU] = 400 * time.Millisecond
	res, err := Run(context.Background(), r.env)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PickedCPU || res.Choice.Accelerator != accel.CPU || res.Choice.Model != accel.ModelParakeet {
		t.Fatalf("result = %+v", res)
	}
	got := r.cfgText()
	for _, want := range []string{"# my notes", "# Set by `footman setup` on 2026-11-25:", "slower than the CPU path", "# Delete this line", "accelerator = \"cpu\""} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
	c, err := config.Load(r.cfg)
	if err != nil {
		t.Fatalf("saved config is invalid: %v\n%s", err, got)
	}
	// The daemon's own choice now lands on the CPU row.
	ch, err := accel.Choose(r.env.Hardware, accel.Request{Engine: c.String("speech.stt.engine"), Model: "auto", Accelerator: c.String("speech.stt.accelerator")}, engines())
	if err != nil || ch.Accelerator != accel.CPU {
		t.Fatalf("daemon would choose %+v, %v", ch, err)
	}
}

func TestBrokenGPUFallsBackToCPU(t *testing.T) {
	for name, set := range map[string]func(*rig){
		"open fails":       func(r *rig) { r.openErr[accel.Vulkan] = errors.New("no Vulkan device") },
		"transcribe fails": func(r *rig) { r.runErr[accel.Vulkan] = errors.New("device lost") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "")
			r.each[accel.CPU] = 300 * time.Millisecond
			set(r)
			res, err := Run(context.Background(), r.env)
			if err != nil {
				t.Fatal(err)
			}
			if !res.PickedCPU || !strings.Contains(r.cfgText(), "did not work") {
				t.Fatalf("result %+v config:\n%s", res, r.cfgText())
			}
		})
	}
}

func TestBrokenCPUKeepsGPU(t *testing.T) {
	r := newRig(t, "")
	r.each[accel.Vulkan] = 100 * time.Millisecond
	r.runErr[accel.CPU] = errors.New("boom")
	res, err := Run(context.Background(), r.env)
	if err != nil || res.PickedCPU || strings.Contains(r.cfgText(), "accelerator") {
		t.Fatalf("res %+v err %v cfg %q", res, err, r.cfgText())
	}
}

func TestPinnedAcceleratorIsWarmedButNotCompared(t *testing.T) {
	r := newRig(t, "[speech.stt]\naccelerator = \"vulkan\"\n")
	r.each[accel.Vulkan] = 5 * time.Second // slow, but the user chose it
	res, err := Run(context.Background(), r.env)
	if err != nil {
		t.Fatal(err)
	}
	if res.PickedCPU || len(r.opened) != 1 || r.opened[0].Accelerator != accel.Vulkan {
		t.Fatalf("res %+v opened %v", res, r.opened)
	}
	if strings.Contains(strings.Join(r.fetched, ","), "parakeet") {
		t.Fatalf("fetched the CPU model for nothing: %v", r.fetched)
	}
}

func TestCPUOnlyMachineFetchesAndSkipsWarmup(t *testing.T) {
	r := newRig(t, "")
	r.env.Hardware = accel.Hardware{RAM: 16 << 30, CPUs: 8}
	res, err := Run(context.Background(), r.env)
	if err != nil {
		t.Fatal(err)
	}
	if res.Choice.Engine != accel.EngineSherpaParakeet || len(r.opened) != 0 {
		t.Fatalf("res %+v opened %v", res, r.opened)
	}
	if strings.Join(r.fetched, ",") != "parakeet-tdt-0.6b-v2,silero-vad" {
		t.Fatalf("fetched %v", r.fetched)
	}
}

func TestOfflineWithModelsPresent(t *testing.T) {
	r := newRig(t, "")
	r.each[accel.Vulkan], r.each[accel.CPU] = 100*time.Millisecond, 500*time.Millisecond
	if _, err := Run(context.Background(), r.env); err != nil { // first run downloads
		t.Fatal(err)
	}
	r.fetched, r.records, r.opened = nil, nil, nil
	offline := errors.New("network is unreachable")
	r.env.Fetch = func(context.Context, models.Model, string, models.FetchOptions) (string, error) { return "", offline }
	if _, err := Run(context.Background(), r.env); err != nil {
		t.Fatalf("setup needed the network with the models present: %v", err)
	}
	if len(r.opened) != 2 {
		t.Fatalf("warm-up/benchmark did not run offline: %v", r.opened)
	}
	// They are still on the receipt.
	if !strings.Contains(strings.Join(r.records, " "), "ggml-large-v3-turbo-q5_0.bin|whisper-turbo-q5|false") {
		t.Fatalf("records: %v", r.records)
	}
}

func TestDownloadFailureNamesTheWayOut(t *testing.T) {
	r := newRig(t, "")
	r.env.Fetch = func(context.Context, models.Model, string, models.FetchOptions) (string, error) {
		return "", errors.New("dial tcp: no route to host")
	}
	_, err := Run(context.Background(), r.env)
	if err == nil || !strings.Contains(err.Error(), "no route to host") || !strings.Contains(err.Error(), "--skip-models") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordsModelsBeforeDownload(t *testing.T) {
	r := newRig(t, "")
	r.env.Hardware = accel.Hardware{RAM: 16 << 30}
	if _, err := Run(context.Background(), r.env); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.records, " ")
	for _, want := range []string{"models|parakeet-tdt-0.6b-v2|true", "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8|parakeet-tdt-0.6b-v2|false", "silero_vad.onnx|silero-vad|false"} {
		if !strings.Contains(got, want) {
			t.Errorf("records lack %q: %s", want, got)
		}
	}
}

func TestNoEnginesSkips(t *testing.T) {
	r := newRig(t, "")
	r.env.Engines = nil
	res, err := Run(context.Background(), r.env)
	if err != nil || res.Skipped == "" || len(r.fetched) != 0 {
		t.Fatalf("res %+v err %v fetched %v", res, err, r.fetched)
	}
}

func TestBadConfigIsAnError(t *testing.T) {
	r := newRig(t, "[speech.stt]\naccelerator = \"tpu\"\n")
	if _, err := Run(context.Background(), r.env); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBenchmarkRunsAreBounded(t *testing.T) {
	r := newRig(t, "")
	r.each[accel.Vulkan], r.each[accel.CPU] = 10*time.Second, 20*time.Second // each run blows the budget
	res, err := Run(context.Background(), r.env)
	if err != nil {
		t.Fatal(err)
	}
	if res.GPU.Runs != 1 || res.CPU.Runs != 1 {
		t.Fatalf("runs gpu=%d cpu=%d, want 1 each", res.GPU.Runs, res.CPU.Runs)
	}
	r2 := newRig(t, "")
	r2.each[accel.Vulkan], r2.each[accel.CPU] = time.Millisecond, 2*time.Millisecond
	res, _ = Run(context.Background(), r2.env)
	if res.GPU.Runs != 3 {
		t.Fatalf("runs = %d, want MaxRuns 3", res.GPU.Runs)
	}
}

func TestClip(t *testing.T) {
	c := Clip(5 * time.Second)
	if c.Duration() != 5*time.Second {
		t.Fatalf("duration %v", c.Duration())
	}
	var peak float32
	for _, s := range c.Samples {
		if s > peak {
			peak = s
		} else if -s > peak {
			peak = -s
		}
	}
	if peak < 0.05 || peak > 1 {
		t.Fatalf("peak %v", peak)
	}
}
