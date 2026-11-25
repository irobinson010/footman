package whispercpp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/registry"
)

func TestOptionsDecodeAndDefaults(t *testing.T) {
	var o Options
	err := registry.Map{"model": "whisper-turbo-q5", "models_dir": "/m", "accelerator": "vulkan", "language": "es",
		"cache_dir": "/c", "device": 1, "threads": 6, "warmup": false}.Decode(&o)
	if err != nil {
		t.Fatal(err)
	}
	if o.Model != "whisper-turbo-q5" || *o.Device != 1 || o.threads(Vulkan) != 6 || o.warmup() {
		t.Errorf("decoded %+v", o)
	}
	if err := (registry.Map{"modell": "x"}).Decode(&o); err == nil {
		t.Error("a misspelt key was accepted")
	}

	d := Options{}.withDefaults()
	if d.Model != "whisper-base.en" || d.Accelerator != "auto" || d.Language != "en" {
		t.Errorf("defaults %+v", d)
	}
	if d.threads(Vulkan) != 4 || d.threads(CPU) < 1 || d.threads(CPU) > 16 {
		t.Errorf("threads: gpu %d, cpu %d", d.threads(Vulkan), d.threads(CPU))
	}
	if !d.warmup() {
		t.Error("warm-up is off by default")
	}
}

func TestBackends(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"backends/cpu/libggml-cpu-haswell.so", "backends/cpu/libggml-cpu-zen4.so", "backends/vulkan/libggml-vulkan.so"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "backends", "cuda"), 0o755); err != nil { // empty: not installed
		t.Fatal(err)
	}
	got := installedBackends(dir)
	if strings.Join(got, " ") != "vulkan cpu" {
		t.Fatalf("installed %v", got)
	}
	for want, exp := range map[string]string{"auto": Vulkan, "gpu": Vulkan, "cpu": CPU, "vulkan": Vulkan, "cuda": "", "tpu": ""} {
		acc, err := resolveAccelerator(want, got)
		if acc != exp || (exp == "") != (err != nil) {
			t.Errorf("resolve %q = %q, %v; want %q", want, acc, err, exp)
		}
	}
	if acc, err := resolveAccelerator("auto", []string{CPU}); acc != CPU || err != nil {
		t.Errorf("auto with only cpu = %q, %v", acc, err)
	}
	if _, err := resolveAccelerator("gpu", []string{CPU}); err == nil {
		t.Error("gpu with only cpu resolved")
	}
	if got := installedBackends(t.TempDir()); len(got) != 0 {
		t.Errorf("empty folder: %v", got)
	}
}

func TestSetShaderCache(t *testing.T) {
	env := map[string]string{"MESA_SHADER_CACHE_DIR": "/mine"}
	get := func(k string) string { return env[k] }
	set := func(k, v string) error { env[k] = v; return nil }
	cache := t.TempDir()
	dir, err := setShaderCache(cache, get, set)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(cache, "vulkan") {
		t.Errorf("dir %s", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("shader cache folder not created: %v", err)
	}
	if env["__GL_SHADER_DISK_CACHE_PATH"] != dir || env["__GL_SHADER_DISK_CACHE"] != "1" || env["__GL_SHADER_DISK_CACHE_SKIP_CLEANUP"] != "1" {
		t.Errorf("NVIDIA variables: %v", env)
	}
	if env["MESA_SHADER_CACHE_DIR"] != "/mine" {
		t.Error("a variable the user set was overridden")
	}
	if dir, err := setShaderCache("", get, set); dir != "" || err != nil {
		t.Errorf("no cache folder: %q, %v", dir, err)
	}
}
