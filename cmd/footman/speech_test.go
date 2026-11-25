package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/registry"
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
	if o, ok := vadOptions(defaultVAD, dirs).(registry.Map); !ok || o["models_dir"] != "/m" {
		t.Errorf("silero options %v", o)
	}
	if vadOptions(fakeName, dirs) != nil {
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
