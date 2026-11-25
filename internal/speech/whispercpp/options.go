// Package whispercpp is the whisper.cpp speech-to-text engine (ADR-0024),
// registered as "whispercpp" in speech.STTs.
//
// The engine itself needs cgo and a whisper.cpp built with
// scripts/native/whispercpp.sh, so it is compiled only with the `native`
// build tag. whisper.cpp is built with GGML_BACKEND_DL: the CPU, Vulkan and
// CUDA backends are plugins in backends/<name>/ next to libwhisper, and the
// engine loads the one the accelerator option names at run time (ADR-0027).
// One binary therefore serves every accelerator, and the CPU path never
// initializes a GPU.
//
// This file holds the options and helpers that need no cgo, so they build and
// are tested everywhere.
package whispercpp

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Name is the engine's registry name.
const Name = "whispercpp"

// Options configure the engine. They arrive through registry.Config from the
// daemon (which fills them from speech.stt.* and the user's folders).
type Options struct {
	// Model is a model catalog ID (see internal/speech/models) or a path to
	// a ggml file. Default whisper-base.en.
	Model string `json:"model"`
	// ModelsDir is the models folder catalog IDs are looked up in.
	ModelsDir string `json:"models_dir"`
	// Accelerator is cpu, vulkan, cuda, or auto/gpu (Vulkan, else CUDA,
	// else CPU, among the backends that are installed). Default auto.
	Accelerator string `json:"accelerator"`
	// Device is the index of the GPU to use among the backend's GPUs, or -1
	// (the default) for the first discrete GPU, else the first integrated
	// one. GGML_VK_VISIBLE_DEVICES and CUDA_VISIBLE_DEVICES still apply.
	Device *int `json:"device"`
	// Language is the spoken language code, or "auto" to detect it. English-
	// only models always use "en". Default "en".
	Language string `json:"language"`
	// Threads is the number of CPU threads; 0 picks 4 on a GPU and half the
	// logical CPUs (at most 16) on the CPU.
	Threads int `json:"threads"`
	// CacheDir holds the persisted Vulkan shader cache (in vulkan/). Empty
	// leaves the driver's own default.
	CacheDir string `json:"cache_dir"`
	// Warmup runs a short transcription at load, so one-time costs (GPU
	// shader compilation, and on the CPU a first run that takes several
	// times as long as later ones) never land on the first dictation.
	// Default true.
	Warmup *bool `json:"warmup"`
	// BackendDir is the folder holding backends/<name>/; empty means the
	// folder libwhisper was loaded from.
	BackendDir string `json:"backend_dir"`
	// FlashAttention turns on flash attention on a GPU (default true, as in
	// whisper.cpp).
	FlashAttention *bool `json:"flash_attention"`
	// Verbose passes whisper.cpp's informational log lines to stderr;
	// otherwise only warnings and errors are shown.
	Verbose bool `json:"verbose"`
}

// Accelerator names the engine understands.
const (
	CPU    = "cpu"
	Vulkan = "vulkan"
	CUDA   = "cuda"
)

// backendNames lists the backends in the order "auto" prefers them.
var backendNames = []string{Vulkan, CUDA, CPU}

// withDefaults fills the zero fields.
func (o Options) withDefaults() Options {
	if o.Model == "" {
		o.Model = "whisper-base.en"
	}
	if o.Accelerator == "" {
		o.Accelerator = "auto"
	}
	if o.Language == "" {
		o.Language = "en"
	}
	return o
}

// threads returns the thread count for accelerator acc.
func (o Options) threads(acc string) int {
	if o.Threads > 0 {
		return o.Threads
	}
	if acc != CPU {
		return 4
	}
	return max(1, min(16, runtime.NumCPU()/2))
}

// warmup reports whether to warm up at load.
func (o Options) warmup() bool { return o.Warmup == nil || *o.Warmup }

// installedBackends lists the backends with a plugin under dir/backends.
func installedBackends(dir string) []string {
	var out []string
	for _, b := range backendNames {
		libs, _ := filepath.Glob(filepath.Join(dir, "backends", b, "*ggml-"+b+"*"))
		if len(libs) > 0 {
			out = append(out, b)
		}
	}
	return out
}

// resolveAccelerator turns the accelerator option into a backend name given
// the installed backends.
func resolveAccelerator(want string, installed []string) (string, error) {
	switch want {
	case "auto", "gpu":
		for _, b := range backendNames {
			if b == CPU && want == "gpu" {
				break
			}
			if slices.Contains(installed, b) {
				return b, nil
			}
		}
	case CPU, Vulkan, CUDA:
		if slices.Contains(installed, want) {
			return want, nil
		}
	default:
		return "", fmt.Errorf("unknown accelerator %q (auto, gpu, cpu, vulkan, cuda)", want)
	}
	return "", fmt.Errorf("no whisper.cpp backend for %q is installed (have: %s); build it with scripts/native/whispercpp.sh",
		want, strings.Join(installed, ", "))
}

// Environment variables that move GPU drivers' shader caches.
var shaderCacheEnv = []struct{ name, value string }{
	// NVIDIA's proprietary driver (OpenGL and Vulkan).
	{"__GL_SHADER_DISK_CACHE", "1"},
	{"__GL_SHADER_DISK_CACHE_PATH", ""},
	{"__GL_SHADER_DISK_CACHE_SKIP_CLEANUP", "1"},
	// Mesa: RADV (AMD), ANV (Intel), NVK.
	{"MESA_SHADER_CACHE_DIR", ""},
}

// ShaderCacheDir is where the Vulkan shader cache goes under cacheDir.
func ShaderCacheDir(cacheDir string) string { return filepath.Join(cacheDir, "vulkan") }

// setShaderCache points the GPU drivers' shader caches at
// ShaderCacheDir(cacheDir), so the one-time pipeline compilation is kept in
// Footman's cache folder (and removed with it) rather than wherever the
// driver defaults to. Variables the user has already set are left alone. It
// must run before the Vulkan backend is loaded.
func setShaderCache(cacheDir string, getenv func(string) string, setenv func(string, string) error) (string, error) {
	if cacheDir == "" {
		return "", nil
	}
	dir := ShaderCacheDir(cacheDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, e := range shaderCacheEnv {
		if getenv(e.name) != "" {
			continue
		}
		v := e.value
		if v == "" {
			v = dir
		}
		if err := setenv(e.name, v); err != nil {
			return "", err
		}
	}
	return dir, nil
}
