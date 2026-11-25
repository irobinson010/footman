package accel

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Detect inspects this machine. It reads files only and never fails; what it
// cannot find is left at its zero value. Outside Linux it reports only the
// CPU count for now, so the choice falls to the CPU rows.
func Detect() Hardware {
	if runtime.GOOS != "linux" {
		return Hardware{CPUs: runtime.NumCPU()}
	}
	h := detectLinux("/")
	h.CPUs = runtime.NumCPU()
	return h
}

var vendors = map[string]string{"0x10de": "nvidia", "0x1002": "amd", "0x8086": "intel"}

// detectLinux reads /proc and /sys under root (a test seam).
func detectLinux(root string) Hardware {
	at := func(p string) string { return filepath.Join(root, p) }
	var h Hardware
	h.RAM = memTotal(at("proc/meminfo"))
	icds := vulkanICDs(root)
	cards, _ := filepath.Glob(at("sys/class/drm/card[0-9]*"))
	seen := map[string]bool{}
	for _, c := range cards {
		if strings.Contains(filepath.Base(c), "-") { // card0-DP-1 and the like are connectors
			continue
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(c, "device"))
		if err != nil || seen[dev] {
			continue
		}
		seen[dev] = true
		if !strings.HasPrefix(read(filepath.Join(dev, "class")), "0x03") { // display controller
			continue
		}
		vid := read(filepath.Join(dev, "vendor"))
		g := GPU{Vendor: vid, Device: read(filepath.Join(dev, "device"))}
		if v, ok := vendors[vid]; ok {
			g.Vendor = v
		}
		if drv, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			g.Driver = filepath.Base(drv)
		}
		g.Integrated = integrated(g, read(filepath.Join(dev, "mem_info_vram_total")))
		g.Vulkan = hasICD(g, icds)
		h.GPUs = append(h.GPUs, g)
	}
	h.VulkanLoader = findLib(root, "libvulkan.so.1")
	_, err := os.Stat(at("proc/driver/nvidia/version"))
	h.CUDA = err == nil && findLib(root, "libcuda.so.1")
	return h
}

func read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func memTotal(p string) uint64 {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

// integrated guesses whether a GPU shares system memory: Intel GPUs other
// than Arc, and AMD GPUs with a small VRAM carve-out (APUs).
func integrated(g GPU, vram string) bool {
	switch g.Vendor {
	case "intel":
		d := strings.ToLower(g.Device)
		arc := strings.HasPrefix(d, "0x56") || strings.HasPrefix(d, "0xe2")
		return !arc
	case "amd":
		n, err := strconv.ParseUint(vram, 10, 64)
		return err == nil && n > 0 && n <= 2<<30
	}
	return false
}

// vulkanICDs returns "<manifest> <library>" for each installed Vulkan
// driver, lower-cased.
func vulkanICDs(root string) []string {
	var out []string
	for _, d := range []string{"usr/share/vulkan/icd.d", "etc/vulkan/icd.d", "usr/local/share/vulkan/icd.d"} {
		files, _ := filepath.Glob(filepath.Join(root, d, "*.json"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var icd struct {
				ICD struct {
					LibraryPath string `json:"library_path"`
				} `json:"ICD"`
			}
			if json.Unmarshal(b, &icd) != nil {
				continue
			}
			out = append(out, strings.ToLower(filepath.Base(f)+" "+filepath.Base(icd.ICD.LibraryPath)))
		}
	}
	return out
}

// hasICD reports whether a Vulkan driver for g is installed. Mesa installs
// its drivers whatever the hardware, so the driver must match the vendor and,
// for NVIDIA, the kernel driver in use.
func hasICD(g GPU, icds []string) bool {
	var want []string
	switch g.Vendor {
	case "nvidia":
		want = []string{"nvidia"}
		if g.Driver == "nouveau" {
			want = []string{"nouveau"}
		}
	case "amd":
		want = []string{"radeon", "amd_icd", "amdvlk"}
	case "intel":
		want = []string{"intel"}
	default:
		return false
	}
	for _, icd := range icds {
		for _, w := range want {
			if strings.Contains(icd, w) {
				return true
			}
		}
	}
	return false
}

// findLib looks for a shared library in the usual places (and, on the real
// root, LD_LIBRARY_PATH).
func findLib(root, name string) bool {
	dirs := []string{
		"usr/lib/x86_64-linux-gnu", "usr/lib/aarch64-linux-gnu", "usr/lib64", "usr/lib",
		"lib/x86_64-linux-gnu", "lib/aarch64-linux-gnu", "lib64", "lib", "usr/local/lib",
		"usr/lib/wsl/lib",
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(root, d, name)); err == nil {
			return true
		}
	}
	if root != "/" {
		return false
	}
	for _, d := range filepath.SplitList(os.Getenv("LD_LIBRARY_PATH")) {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, name)); err == nil {
			return true
		}
	}
	return false
}
