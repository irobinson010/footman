package accel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	full    = map[string][]string{EngineWhisperCpp: {CPU, Vulkan, CUDA}, EngineSherpaParakeet: {CPU}}
	cpuOnly = map[string][]string{EngineWhisperCpp: {CPU}, EngineSherpaParakeet: {CPU}}
	nvidia  = GPU{Vendor: "nvidia", Driver: "nvidia", Vulkan: true}
)

func TestChooseTable(t *testing.T) {
	gpuBox := Hardware{RAM: 64 << 30, GPUs: []GPU{nvidia}, VulkanLoader: true, CUDA: true}
	bigCPU := Hardware{RAM: 16 << 30}
	smallCPU := Hardware{RAM: 4 << 30}
	for _, tc := range []struct {
		name  string
		hw    Hardware
		req   Request
		avail map[string][]string
		want  string // engine model accelerator
	}{
		{"gpu", gpuBox, Request{}, full, "whispercpp whisper-turbo-q5 vulkan"},
		{"gpu, cuda asked", gpuBox, Request{Accelerator: "cuda"}, full, "whispercpp whisper-turbo-q5 cuda"},
		{"gpu, cpu asked", gpuBox, Request{Accelerator: "cpu"}, full, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"gpu but build has no vulkan", gpuBox, Request{}, map[string][]string{EngineWhisperCpp: {CPU, CUDA}, EngineSherpaParakeet: {CPU}}, "whispercpp whisper-turbo-q5 cuda"},
		{"gpu, cpu-only build", gpuBox, Request{}, cpuOnly, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"integrated vulkan counts", Hardware{RAM: 16 << 30, VulkanLoader: true, GPUs: []GPU{{Vendor: "intel", Integrated: true, Vulkan: true}}}, Request{}, full, "whispercpp whisper-turbo-q5 vulkan"},
		{"gpu without loader", Hardware{RAM: 16 << 30, GPUs: []GPU{nvidia}}, Request{}, map[string][]string{EngineWhisperCpp: {CPU, Vulkan}, EngineSherpaParakeet: {CPU}}, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"cpu, 16 GB", bigCPU, Request{}, full, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"cpu, 4 GB", smallCPU, Request{}, full, "whispercpp whisper-base.en cpu"},
		{"cpu, spanish", bigCPU, Request{Language: "es"}, full, "whispercpp whisper-small cpu"},
		{"cpu, en-GB", bigCPU, Request{Language: "en-GB"}, full, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"gpu, spanish", gpuBox, Request{Language: "es"}, full, "whispercpp whisper-turbo-q5 vulkan"},
		{"cpu, no parakeet in build", bigCPU, Request{}, map[string][]string{EngineWhisperCpp: {CPU}}, "whispercpp whisper-base.en cpu"},
		{"cpu, 4 GB, only parakeet", smallCPU, Request{}, map[string][]string{EngineSherpaParakeet: {CPU}}, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"engine fixed", gpuBox, Request{Engine: "sherpa-parakeet"}, full, "sherpa-parakeet parakeet-tdt-0.6b-v2 cpu"},
		{"engine fixed, cpu", bigCPU, Request{Engine: "whispercpp"}, full, "whispercpp whisper-base.en cpu"},
		{"model fixed", gpuBox, Request{Model: "whisper-small.en"}, full, "whispercpp whisper-small.en vulkan"},
		{"auto spelled out", gpuBox, Request{Engine: "auto", Model: "auto", Accelerator: "auto"}, full, "whispercpp whisper-turbo-q5 vulkan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Choose(tc.hw, tc.req, tc.avail)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Engine + " " + c.Model + " " + c.Accelerator; got != tc.want {
				t.Errorf("got %s (%s), want %s", got, c.Reason, tc.want)
			}
		})
	}
}

func TestChooseErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hw    Hardware
		req   Request
		avail map[string][]string
		want  string
	}{
		{"nothing built", Hardware{}, Request{}, nil, "build with -tags native"},
		{"engine missing", Hardware{}, Request{Engine: "whisper-local"}, full, "not in this build"},
		{"vulkan asked, none", Hardware{RAM: 8 << 30}, Request{Accelerator: "vulkan"}, full, "not available"},
		{"bad accelerator", Hardware{}, Request{Accelerator: "tpu"}, full, "unknown accelerator"},
		{"parakeet on gpu", Hardware{GPUs: []GPU{nvidia}, VulkanLoader: true}, Request{Engine: "sherpa-parakeet", Accelerator: "gpu"}, full, "not available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Choose(tc.hw, tc.req, tc.avail)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// fakeRoot builds a /proc and /sys tree like a desktop with an NVIDIA card,
// an AMD APU's integrated GPU and Mesa plus NVIDIA Vulkan drivers.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/meminfo", "MemTotal:       65536000 kB\nMemFree: 1 kB\n")
	write("proc/driver/nvidia/version", "NVRM version: 595\n")
	write("usr/lib/x86_64-linux-gnu/libvulkan.so.1", "")
	write("usr/lib/x86_64-linux-gnu/libcuda.so.1", "")
	write("usr/share/vulkan/icd.d/nvidia_icd.json", `{"ICD":{"library_path":"libGLX_nvidia.so.0"}}`)
	write("usr/share/vulkan/icd.d/radeon_icd.json", `{"ICD":{"library_path":"/usr/lib/libvulkan_radeon.so"}}`)
	write("usr/share/vulkan/icd.d/lvp_icd.json", `{"ICD":{"library_path":"libvulkan_lvp.so"}}`)
	for _, d := range []struct{ pci, vendor, device, driver, vram string }{
		{"0000:01:00.0", "0x10de", "0x2704", "nvidia", ""},
		{"0000:7a:00.0", "0x1002", "0x164e", "amdgpu", "536870912"},
	} {
		dev := filepath.Join(root, "sys/devices/pci", d.pci)
		write(filepath.Join("sys/devices/pci", d.pci, "class"), "0x030000\n")
		write(filepath.Join("sys/devices/pci", d.pci, "vendor"), d.vendor+"\n")
		write(filepath.Join("sys/devices/pci", d.pci, "device"), d.device+"\n")
		if d.vram != "" {
			write(filepath.Join("sys/devices/pci", d.pci, "mem_info_vram_total"), d.vram)
		}
		link("/sys/bus/pci/drivers/"+d.driver, filepath.Join("sys/devices/pci", d.pci, "driver"))
		card := map[string]string{"0x10de": "card1", "0x1002": "card2"}[d.vendor]
		link(dev, "sys/class/drm/"+card+"/device")
	}
	link(filepath.Join(root, "sys/devices/pci/0000:01:00.0"), "sys/class/drm/card1-DP-1/device")
	return root
}

func TestDetectLinux(t *testing.T) {
	h := detectLinux(fakeRoot(t))
	if h.RAM != 65536000<<10 {
		t.Errorf("RAM %d", h.RAM)
	}
	if !h.VulkanLoader || !h.CUDA || !h.Vulkan() {
		t.Errorf("loader %v cuda %v vulkan %v", h.VulkanLoader, h.CUDA, h.Vulkan())
	}
	if len(h.GPUs) != 2 {
		t.Fatalf("GPUs %+v", h.GPUs)
	}
	nv, amd := h.GPUs[0], h.GPUs[1]
	if nv.Vendor != "nvidia" || nv.Driver != "nvidia" || nv.Integrated || !nv.Vulkan {
		t.Errorf("nvidia: %+v", nv)
	}
	if amd.Vendor != "amd" || !amd.Integrated || !amd.Vulkan {
		t.Errorf("amd: %+v", amd)
	}
	if s := h.String(); !strings.Contains(s, "nvidia 0x2704 vulkan") {
		t.Errorf("String() = %q", s)
	}
}

func TestDetectEmpty(t *testing.T) {
	h := detectLinux(t.TempDir())
	if h.RAM != 0 || len(h.GPUs) != 0 || h.Vulkan() || h.CUDA {
		t.Errorf("%+v", h)
	}
	if c, err := Choose(h, Request{}, full); err != nil || c.Engine != EngineSherpaParakeet {
		t.Errorf("unknown RAM: %v %v", c, err)
	}
}

func TestDetectThisMachine(t *testing.T) {
	// Smoke test only: whatever the machine, Detect must not fail or hang.
	t.Log(Detect())
}

func TestRegisterEngine(t *testing.T) {
	RegisterEngine("test-engine", func() []string { return []string{CPU} })
	t.Cleanup(func() {
		mu.Lock()
		delete(engines, "test-engine")
		mu.Unlock()
	})
	if got := Engines()["test-engine"]; len(got) != 1 || got[0] != CPU {
		t.Errorf("Engines() = %v", Engines())
	}
	defer func() {
		if recover() == nil {
			t.Error("duplicate registration did not panic")
		}
	}()
	RegisterEngine("test-engine", func() []string { return nil })
}
