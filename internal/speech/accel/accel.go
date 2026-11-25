// Package accel detects the machine's speech accelerators and picks the
// default speech-to-text engine, model and accelerator for it (ADR-0010,
// ADR-0024).
//
// Detection reads driver and file-system information only (sysfs, /proc, the
// Vulkan and CUDA loader libraries); it starts no GPU work, so it is cheap to
// run at every daemon start. Engines say which accelerators their build can
// use with RegisterEngine, so the choice never names an engine or backend the
// binary does not have.
package accel

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Accelerator names, as used in config (speech.stt.accelerator).
const (
	Auto   = "auto"
	CPU    = "cpu"
	AnyGPU = "gpu" // any GPU: Vulkan, else CUDA
	Vulkan = "vulkan"
	CUDA   = "cuda"
)

// Engine and model names the choice can return. They match the names the
// native engines register under and the model catalog.
const (
	EngineWhisperCpp     = "whispercpp"
	EngineSherpaParakeet = "sherpa-parakeet"

	ModelWhisperTurboQ5 = "whisper-turbo-q5"
	ModelWhisperBaseEn  = "whisper-base.en"
	ModelWhisperSmall   = "whisper-small"
	ModelParakeet       = "parakeet-tdt-0.6b-v2"
)

// MinRAMForParakeet is the RAM below which the CPU default is whisper.cpp
// base.en instead of Parakeet (ADR-0024: Parakeet peaks at about 1.7 GB).
const MinRAMForParakeet = 8 << 30

// GPU is one graphics adapter.
type GPU struct {
	// Vendor is "nvidia", "amd", "intel" or the PCI vendor ID.
	Vendor string
	// Device is the PCI device ID, such as "0x2704".
	Device string
	// Driver is the kernel driver bound to it, such as "nvidia" or "amdgpu".
	Driver string
	// Integrated is a best guess that the GPU shares system memory.
	Integrated bool
	// Vulkan is true when a Vulkan driver (ICD) for it is installed.
	Vulkan bool
}

// Hardware is what Detect found.
type Hardware struct {
	// RAM is the total system memory in bytes, or 0 if unknown.
	RAM uint64
	// CPUs is the number of logical CPUs.
	CPUs int
	// GPUs lists the display adapters.
	GPUs []GPU
	// VulkanLoader is true when libvulkan can be found.
	VulkanLoader bool
	// CUDA is true when the NVIDIA driver and libcuda are present.
	CUDA bool
}

// Vulkan reports whether some GPU can run Vulkan compute: the loader is
// installed and a GPU has a driver for it.
func (h Hardware) Vulkan() bool {
	if !h.VulkanLoader {
		return false
	}
	for _, g := range h.GPUs {
		if g.Vulkan {
			return true
		}
	}
	return false
}

// String summarizes the hardware for logs.
func (h Hardware) String() string {
	var gpus []string
	for _, g := range h.GPUs {
		s := g.Vendor + " " + g.Device
		if g.Integrated {
			s += " (integrated)"
		}
		if g.Vulkan {
			s += " vulkan"
		}
		gpus = append(gpus, s)
	}
	if len(gpus) == 0 {
		gpus = []string{"none"}
	}
	return fmt.Sprintf("%d CPUs, %.1f GB RAM, GPUs: %s; vulkan loader %v, cuda %v",
		h.CPUs, float64(h.RAM)/(1<<30), strings.Join(gpus, ", "), h.VulkanLoader, h.CUDA)
}

var (
	mu      sync.RWMutex
	engines = map[string]func() []string{}
)

// RegisterEngine records that the STT engine name is in this build and can
// use the accelerators accels returns (CPU, Vulkan, CUDA). accels is called
// when a choice is made, so it can look for backend libraries at run time.
// The native engine packages call it from init.
func RegisterEngine(name string, accels func() []string) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := engines[name]; dup {
		panic("accel: engine " + name + " registered twice")
	}
	engines[name] = accels
}

// Engines returns, for each registered engine, the accelerators it can use.
func Engines() map[string][]string {
	mu.RLock()
	defer mu.RUnlock()
	out := map[string][]string{}
	for n, f := range engines {
		out[n] = f()
	}
	return out
}

// Request is what the config asks for; "" or "auto" leaves a field to Choose.
type Request struct {
	// Engine is speech.stt.engine.
	Engine string
	// Model is speech.stt.model.
	Model string
	// Accelerator is speech.stt.accelerator: auto, cpu, gpu, vulkan or cuda.
	Accelerator string
	// Language is the user's language (user.language); "" means English.
	Language string
}

// Choice is the engine, model and accelerator to use.
type Choice struct {
	Engine, Model, Accelerator string
	// Reason explains the choice, for logs and status.
	Reason string
}

func (c Choice) String() string {
	return fmt.Sprintf("%s %s on %s (%s)", c.Engine, c.Model, c.Accelerator, c.Reason)
}

func auto(s string) bool { return s == "" || s == Auto }

func english(lang string) bool {
	lang = strings.ToLower(lang)
	return lang == "" || lang == "en" || strings.HasPrefix(lang, "en-") || strings.HasPrefix(lang, "en_")
}

// Choose applies the ADR-0024 table to hw, within the engines and
// accelerators available (normally Engines()), honouring whatever req fixes:
//
//	GPU with Vulkan               whispercpp  whisper-turbo-q5      vulkan
//	CPU only, >= 8 GB RAM         sherpa-parakeet parakeet-tdt-0.6b-v2 cpu
//	CPU only, < 8 GB RAM          whispercpp  whisper-base.en       cpu
//	non-English, no GPU           whispercpp  whisper-small         cpu
//
// CUDA is used only when asked for ("cuda"), or for "gpu"/"auto" when there
// is no Vulkan but CUDA works. It returns an error when nothing available
// satisfies the request.
func Choose(hw Hardware, req Request, available map[string][]string) (Choice, error) {
	has := func(engine, acc string) bool { return slices.Contains(available[engine], acc) }
	acc := req.Accelerator
	if auto(acc) {
		acc = Auto
	}
	switch acc {
	case Auto, CPU, AnyGPU, Vulkan, CUDA:
	default:
		return Choice{}, fmt.Errorf("unknown accelerator %q (auto, cpu, gpu, vulkan, cuda)", req.Accelerator)
	}
	wantEngine := req.Engine
	if auto(wantEngine) {
		wantEngine = ""
	} else if _, ok := available[wantEngine]; !ok {
		return Choice{}, fmt.Errorf("speech-to-text engine %q is not in this build (have %s)", wantEngine, names(available))
	}
	engineOK := func(e string) bool { return wantEngine == "" || wantEngine == e }
	pick := func(engine, model, a, reason string) Choice {
		if !auto(req.Model) {
			model = req.Model
		}
		return Choice{Engine: engine, Model: model, Accelerator: a, Reason: reason}
	}

	// GPU rows: whisper.cpp only (sherpa-onnx's GPU runtime is not shipped).
	if engineOK(EngineWhisperCpp) && acc != CPU {
		if (acc == Auto || acc == AnyGPU || acc == Vulkan) && hw.Vulkan() && has(EngineWhisperCpp, Vulkan) {
			return pick(EngineWhisperCpp, ModelWhisperTurboQ5, Vulkan, "GPU with Vulkan"), nil
		}
		if (acc == Auto || acc == AnyGPU || acc == CUDA) && hw.CUDA && has(EngineWhisperCpp, CUDA) {
			return pick(EngineWhisperCpp, ModelWhisperTurboQ5, CUDA, "NVIDIA GPU with CUDA"), nil
		}
	}
	if acc != Auto && acc != CPU {
		return Choice{}, fmt.Errorf("accelerator %q is not available: %s; engines in this build: %s", acc, hw, describe(available))
	}

	// CPU rows.
	en := english(req.Language)
	if !en && engineOK(EngineWhisperCpp) && has(EngineWhisperCpp, CPU) {
		return pick(EngineWhisperCpp, ModelWhisperSmall, CPU, "CPU only, language "+req.Language), nil
	}
	parakeet := engineOK(EngineSherpaParakeet) && has(EngineSherpaParakeet, CPU)
	whisper := engineOK(EngineWhisperCpp) && has(EngineWhisperCpp, CPU)
	switch {
	case parakeet && en && (hw.RAM == 0 || hw.RAM >= MinRAMForParakeet || !whisper):
		return pick(EngineSherpaParakeet, ModelParakeet, CPU, "CPU only, enough RAM for Parakeet"), nil
	case whisper && en && parakeet:
		return pick(EngineWhisperCpp, ModelWhisperBaseEn, CPU, "CPU only, under 8 GB RAM"), nil
	case whisper && en:
		return pick(EngineWhisperCpp, ModelWhisperBaseEn, CPU, "CPU only"), nil
	case whisper:
		return pick(EngineWhisperCpp, ModelWhisperSmall, CPU, "CPU only, language "+req.Language), nil
	case parakeet:
		return pick(EngineSherpaParakeet, ModelParakeet, CPU, "CPU only; Parakeet is English-only, but it is the only engine in this build"), nil
	}
	return Choice{}, fmt.Errorf("no speech-to-text engine in this build fits (engines: %s)", describe(available))
}

func names(m map[string][]string) string {
	if len(m) == 0 {
		return "none; build with -tags native"
	}
	var n []string
	for k := range m {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

func describe(m map[string][]string) string {
	if len(m) == 0 {
		return "none; build with -tags native"
	}
	var n []string
	for k, v := range m {
		n = append(n, k+" ["+strings.Join(v, " ")+"]")
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}
