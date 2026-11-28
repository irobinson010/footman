package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
)

func loadConfig(t *testing.T, toml string) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPlanSTT(t *testing.T) {
	dirs := speechDirs{models: "/data/models", cache: "/cache"}
	cpuBox := func() accel.Hardware { return accel.Hardware{RAM: 16 << 30} }

	// An engine outside the accel table (the fake, a cloud engine) is used
	// as named, with no options.
	p, err := planSTT(loadConfig(t, "[speech.stt]\nengine = \"fake\"\n"), dirs, cpuBox)
	if err != nil || p.name != "fake" || p.opts != nil || p.choice != nil {
		t.Fatalf("fake: %+v, %v", p, err)
	}

	// "auto" (the default) applies the ADR-0024 table to the engines in
	// this build: none in a pure-Go build.
	p, err = planSTT(loadConfig(t, ""), dirs, cpuBox)
	if len(accel.Engines()) == 0 {
		if err == nil || !strings.Contains(err.Error(), "-tags native") {
			t.Fatalf("auto without engines: %+v, %v", p, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var opts map[string]any
	if err := p.opts.Decode(&opts); err != nil {
		t.Fatal(err)
	}
	if p.choice == nil || p.name != p.choice.Engine || opts["models_dir"] != "/data/models" || opts["cache_dir"] != "/cache" || opts["accelerator"] != "cpu" {
		t.Errorf("auto: %+v %v", p, opts)
	}
}

func TestVADOptions(t *testing.T) {
	dirs := speechDirs{models: "/m"}
	if o, ok := vadOptions(defaultVAD, dirs, 0.35).(registry.Map); !ok || o["models_dir"] != "/m" || o["min_silence"] != 0.35 {
		t.Errorf("silero options %v", o)
	}
	if vadOptions(fakeName, dirs, 0.5) != nil {
		t.Error("the fake VAD got options")
	}
}

func TestModelsCommand(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"models", "--dir", dir, "--config", cfg, "list"}, &out, &errb); code != 0 {
		t.Fatalf("list = %d: %s", code, errb.String())
	}
	for _, want := range []string{"whisper-turbo-q5", "parakeet-tdt-0.6b-v2", "silero-vad", "CC-BY-4.0", "Models folder: " + dir} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	errb.Reset()
	code := run(context.Background(), []string{"models", "--dir", dir, "--config", cfg, "default"}, &out, &errb)
	if !strings.Contains(out.String(), "hardware: ") || !strings.Contains(out.String(), "engines: ") {
		t.Errorf("default = %d:\n%s%s", code, out.String(), errb.String())
	}

	if code := run(context.Background(), []string{"models", "--dir", dir, "fetch", "no-such-model"}, &out, &errb); code != 1 {
		t.Errorf("fetch of an unknown model = %d", code)
	}
	if code := run(context.Background(), []string{"models", "frobnicate"}, &out, &errb); code != 2 {
		t.Errorf("unknown subcommand = %d", code)
	}
}

func TestTranscribeUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"transcribe"}, &out, &errb); code != 2 {
		t.Errorf("no file = %d", code)
	}
}

// gpuBox is a machine with a Vulkan GPU and 16 GB of RAM; sttEngines are the
// engines of a native build.
func gpuBox() accel.Hardware {
	return accel.Hardware{RAM: 16 << 30, VulkanLoader: true, GPUs: []accel.GPU{{Vendor: "nvidia", Device: "0x2704", Driver: "nvidia", Vulkan: true}}}
}

var sttEngines = map[string][]string{
	accel.EngineWhisperCpp:     {accel.CPU, accel.Vulkan},
	accel.EngineSherpaParakeet: {accel.CPU},
}

type stubSTT struct{ name string }

func (stubSTT) Transcribe(context.Context, speech.Audio) (speech.Transcript, error) {
	return speech.Transcript{}, nil
}

// openFake returns an opener that fails for plans on a GPU accelerator when
// gpuErr is set, and records each attempt's engine and accelerator.
func openFake(gpuErr error, tried *[]string) func(sttPlan) (speech.STT, string, error) {
	return func(p sttPlan) (speech.STT, string, error) {
		acc := p.choice.Accelerator
		*tried = append(*tried, p.name+"/"+acc)
		if acc != accel.CPU && gpuErr != nil {
			return nil, "", gpuErr
		}
		return stubSTT{p.name}, p.name, nil
	}
}

func TestOpenSTTFallsBackToCPUOnAuto(t *testing.T) {
	dirs := speechDirs{models: "/m", cache: "/c"}
	oom := errors.New(`stt "whispercpp": whisper.cpp could not load whisper-turbo-q5 on vulkan` + "\nggml_vulkan: ErrorOutOfDeviceMemory")
	for _, toml := range []string{"", "[speech.stt]\naccelerator = \"auto\"\n"} {
		cfg := loadConfig(t, toml)
		plan, err := planSTTWith(cfg, dirs, gpuBox, sttEngines, nil)
		if err != nil || plan.choice.Accelerator != accel.Vulkan {
			t.Fatalf("plan = %+v, %v", plan, err)
		}
		var tried, logs []string
		logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
		got, err := openSTT(cfg, dirs, gpuBox, sttEngines, plan, openFake(oom, &tried), logf)
		if err != nil {
			t.Fatalf("openSTT: %v", err)
		}
		if got.impl != accel.EngineSherpaParakeet || got.plan.choice.Accelerator != accel.CPU || got.plan.opts == nil {
			t.Errorf("fell back to %+v", got)
		}
		var opts map[string]any
		if err := got.plan.opts.Decode(&opts); err != nil || opts["accelerator"] != "cpu" || opts["model"] != accel.ModelParakeet {
			t.Errorf("fallback options %v, %v", opts, err)
		}
		if want := "whispercpp/vulkan,sherpa-parakeet/cpu"; strings.Join(tried, ",") != want {
			t.Errorf("tried %v, want %s", tried, want)
		}
		if len(logs) != 1 || !strings.HasPrefix(logs[0], "WARNING: ") || strings.Contains(logs[0], "\n") ||
			!strings.Contains(logs[0], "could not load") || !strings.Contains(logs[0], `accelerator = "cpu"`) {
			t.Errorf("want one single-line warning, got %q", logs)
		}
		if p := got.path(); !strings.Contains(p, "sherpa-parakeet") || !strings.Contains(p, "on cpu") || !strings.Contains(p, "fell back from whispercpp on vulkan") {
			t.Errorf("path = %q", p)
		}
	}
}

func TestOpenSTTNoFallbackWhenItWorksOrIsCPU(t *testing.T) {
	dirs := speechDirs{}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	// The GPU opens: no fallback, no warning.
	cfg := loadConfig(t, "")
	plan, _ := planSTTWith(cfg, dirs, gpuBox, sttEngines, nil)
	var tried []string
	got, err := openSTT(cfg, dirs, gpuBox, sttEngines, plan, openFake(nil, &tried), logf)
	if err != nil || got.impl != accel.EngineWhisperCpp || got.fallback != "" || len(logs) != 0 || len(tried) != 1 {
		t.Errorf("working GPU: %+v, %v, logs %q, tried %v", got, err, logs, tried)
	}
	if p := got.path(); !strings.Contains(p, "on vulkan") || strings.Contains(p, "fell back") {
		t.Errorf("path = %q", p)
	}

	// A CPU plan that fails is an error; there is nothing to fall back to.
	cfg = loadConfig(t, "[speech.stt]\naccelerator = \"cpu\"\n")
	plan, _ = planSTTWith(cfg, dirs, gpuBox, sttEngines, nil)
	boom := errors.New("model missing")
	failing := func(sttPlan) (speech.STT, string, error) { return nil, "", boom }
	if _, err := openSTT(cfg, dirs, gpuBox, sttEngines, plan, failing, logf); !errors.Is(err, boom) || len(logs) != 0 {
		t.Errorf("cpu failure: %v, logs %q", err, logs)
	}

	// An engine outside the accel table has no choice, so it is never retried.
	cfg = loadConfig(t, "[speech.stt]\nengine = \"fake\"\n")
	plan, _ = planSTTWith(cfg, dirs, gpuBox, sttEngines, nil)
	if _, err := openSTT(cfg, dirs, gpuBox, sttEngines, plan, failing, logf); !errors.Is(err, boom) {
		t.Errorf("fake failure: %v", err)
	}
}

func TestOpenSTTExplicitAcceleratorFailsLoudly(t *testing.T) {
	dirs := speechDirs{}
	oom := errors.New("ggml_vulkan: ErrorOutOfDeviceMemory")
	for _, acc := range []string{"gpu", "vulkan"} {
		cfg := loadConfig(t, "[speech.stt]\naccelerator = \""+acc+"\"\n")
		plan, err := planSTTWith(cfg, dirs, gpuBox, sttEngines, nil)
		if err != nil {
			t.Fatal(err)
		}
		var tried, logs []string
		logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
		_, err = openSTT(cfg, dirs, gpuBox, sttEngines, plan, openFake(oom, &tried), logf)
		if !errors.Is(err, oom) || !strings.Contains(err.Error(), `"cpu"`) || !strings.Contains(err.Error(), acc) {
			t.Errorf("%s: error %v", acc, err)
		}
		if len(tried) != 1 || len(logs) != 0 {
			t.Errorf("%s: tried %v, logs %q; it must not fall back", acc, tried, logs)
		}
	}
}

func TestOpenSTTFallbackModel(t *testing.T) {
	// A model named for the GPU engine does not suit the CPU engine that
	// accel.Choose picks for the fallback, so the fallback picks its own.
	nolog := func(string, ...any) {}
	var tried []string
	cfg := loadConfig(t, "[speech.stt]\nmodel = \"whisper-turbo-q5\"\n")
	plan, err := planSTTWith(cfg, speechDirs{}, gpuBox, sttEngines, nil)
	if err != nil || plan.choice.Model != "whisper-turbo-q5" {
		t.Fatalf("plan %+v, %v", plan, err)
	}
	got, err := openSTT(cfg, speechDirs{}, gpuBox, sttEngines, plan, openFake(errors.New("no memory"), &tried), nolog)
	if err != nil || got.plan.choice.Engine != accel.EngineSherpaParakeet || got.plan.choice.Model != accel.ModelParakeet {
		t.Errorf("fallback = %+v, %v", got.plan.choice, err)
	}

	// With the engine pinned to whisper.cpp, the CPU fallback keeps it and
	// the user's model.
	cfg = loadConfig(t, "[speech.stt]\nengine = \"whispercpp\"\nmodel = \"whisper-small\"\n")
	plan, _ = planSTTWith(cfg, speechDirs{}, gpuBox, sttEngines, nil)
	got, err = openSTT(cfg, speechDirs{}, gpuBox, sttEngines, plan, openFake(errors.New("no memory"), &tried), nolog)
	if err != nil || got.plan.choice.Engine != accel.EngineWhisperCpp || got.plan.choice.Model != "whisper-small" || got.plan.choice.Accelerator != accel.CPU {
		t.Errorf("pinned fallback = %+v, %v", got.plan.choice, err)
	}
}

func TestOpenSTTCPUFallbackFailsToo(t *testing.T) {
	cfg := loadConfig(t, "")
	plan, _ := planSTTWith(cfg, speechDirs{}, gpuBox, sttEngines, nil)
	gpuErr, cpuErr := errors.New("vulkan out of memory"), errors.New("parakeet model missing")
	open := func(p sttPlan) (speech.STT, string, error) {
		if p.choice.Accelerator != accel.CPU {
			return nil, "", gpuErr
		}
		return nil, "", cpuErr
	}
	_, err := openSTT(cfg, speechDirs{}, gpuBox, sttEngines, plan, open, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), gpuErr.Error()) || !strings.Contains(err.Error(), cpuErr.Error()) {
		t.Errorf("both errors should be reported: %v", err)
	}
}

// newSource is an sttSource for config toml on gpuBox, whose GPU engine
// fails to open while *gpuErr is set.
func newSource(t *testing.T, toml string, gpuErr *error, tried, logs *[]string) *sttSource {
	t.Helper()
	cfg := loadConfig(t, toml)
	plan, err := planSTTWith(cfg, speechDirs{}, gpuBox, sttEngines, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &sttSource{cfg: cfg, hw: gpuBox, avail: sttEngines, first: plan,
		logf: func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) },
		open: func(p sttPlan) (speech.STT, string, error) {
			*tried = append(*tried, p.name+"/"+p.choice.Accelerator)
			if p.choice.Accelerator != accel.CPU && *gpuErr != nil {
				return nil, "", *gpuErr
			}
			return stubSTT{p.name}, p.name, nil
		}}
}

func TestSTTSourceReloadFallsBackAndRetriesTheGPU(t *testing.T) {
	var gpuErr error
	var tried, logs []string
	src := newSource(t, "", &gpuErr, &tried, &logs)

	// Start with a free GPU.
	if o, err := src.Open(); err != nil || o.impl != accel.EngineWhisperCpp || len(logs) != 0 {
		t.Fatalf("open: %+v, %v, %q", o, err, logs)
	}
	if src.Engine() != accel.EngineWhisperCpp || !strings.Contains(src.Path(), "on vulkan") {
		t.Fatalf("engine %q, path %q", src.Engine(), src.Path())
	}

	// The GPU fills up while Footman is idle: the reload falls back, with
	// one warning, and status reflects it.
	gpuErr = errors.New("ggml_vulkan: ErrorOutOfDeviceMemory")
	tried, logs = nil, nil
	stt, err := src.Reload()
	if err != nil || stt != (stubSTT{accel.EngineSherpaParakeet}) {
		t.Fatalf("reload with a full GPU: %v, %v", stt, err)
	}
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "WARNING: ") || strings.Contains(logs[0], "\n") {
		t.Errorf("logs %q, want one warning", logs)
	}
	if src.Engine() != accel.EngineSherpaParakeet || !strings.Contains(src.Path(), "on cpu") || !strings.Contains(src.Path(), "fell back from whispercpp on vulkan") {
		t.Errorf("engine %q, path %q", src.Engine(), src.Path())
	}

	// The next reload tries the GPU first, and says nothing when it is
	// still full.
	tried, logs = nil, nil
	if _, err := src.Reload(); err != nil || strings.Join(tried, ",") != "whispercpp/vulkan,sherpa-parakeet/cpu" || len(logs) != 0 {
		t.Errorf("second reload: %v, tried %v, logs %q", err, tried, logs)
	}

	// Once the GPU is free again, it is used again.
	gpuErr = nil
	tried, logs = nil, nil
	stt, err = src.Reload()
	if err != nil || stt != (stubSTT{accel.EngineWhisperCpp}) || len(logs) != 1 || !strings.Contains(logs[0], "opened again") {
		t.Errorf("reload with a free GPU: %v, %v, logs %q", stt, err, logs)
	}
	if src.Engine() != accel.EngineWhisperCpp || strings.Contains(src.Path(), "fell back") {
		t.Errorf("engine %q, path %q", src.Engine(), src.Path())
	}
}

func TestSTTSourceStartedOnFallbackWarnsOnceAndRetries(t *testing.T) {
	gpuErr := errors.New("out of memory")
	var tried, logs []string
	src := newSource(t, "", &gpuErr, &tried, &logs)
	if o, err := src.Open(); err != nil || o.fallback == "" || len(logs) != 1 {
		t.Fatalf("open: %+v, %v, %q", o, err, logs)
	}
	tried, gpuErr = nil, nil
	if _, err := src.Reload(); err != nil || strings.Join(tried, ",") != "whispercpp/vulkan" {
		t.Errorf("reload: %v, tried %v", err, tried)
	}
}

func TestSTTSourceExplicitAcceleratorKeepsFailingOnReload(t *testing.T) {
	var gpuErr error
	var tried, logs []string
	src := newSource(t, "[speech.stt]\naccelerator = \"vulkan\"\n", &gpuErr, &tried, &logs)
	if _, err := src.Open(); err != nil {
		t.Fatal(err)
	}
	gpuErr = errors.New("ErrorOutOfDeviceMemory")
	tried = nil
	if _, err := src.Reload(); !errors.Is(err, gpuErr) || !strings.Contains(err.Error(), `"cpu"`) || len(tried) != 1 || len(logs) != 0 {
		t.Errorf("reload: %v, tried %v, logs %q", err, tried, logs)
	}
	if src.Engine() != accel.EngineWhisperCpp {
		t.Errorf("engine %q after a failed reload", src.Engine())
	}
}
