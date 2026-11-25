//go:build linux

package main

import (
	"os"
	"os/exec"
	"strings"
)

// rssMB returns current and peak resident set size of this process in MB.
func rssMB() (cur, peak float64) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "VmRSS:":
			cur = kbToMB(f[1])
		case "VmHWM:":
			peak = kbToMB(f[1])
		}
	}
	return
}

func cpuInfo() (name string, flags []string) {
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name":
			if name == "" {
				name = strings.TrimSpace(v)
			}
		case "flags":
			if flags == nil {
				for _, f := range strings.Fields(v) {
					switch f {
					case "avx", "avx2", "avx512f", "avx512_vnni", "avx_vnni", "fma", "f16c":
						flags = append(flags, f)
					}
				}
			}
		}
	}
	return
}

// gpuList names the display adapters the OS sees (information only: this does
// not touch any GPU API).
func gpuList() []string {
	out, err := exec.Command("lspci", "-mm").Output()
	if err != nil {
		return nil
	}
	var g []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "VGA") || strings.Contains(l, "3D controller") || strings.Contains(l, "Display controller") {
			g = append(g, strings.ReplaceAll(strings.TrimSpace(l), "\"", ""))
		}
	}
	return g
}

// vulkanPresent looks for a Vulkan loader and ICD manifests on disk without
// creating a Vulkan instance.
func vulkanPresent() string {
	var found []string
	for _, d := range []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d"} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			found = append(found, e.Name())
		}
	}
	if len(found) == 0 {
		return ""
	}
	return strings.Join(found, ", ")
}

func cudaPresent() string {
	out, err := exec.Command("ldconfig", "-p").Output()
	if err != nil {
		return ""
	}
	var libs []string
	for _, l := range strings.Split(string(out), "\n") {
		for _, n := range []string{"libcudart.so", "libcublas.so", "libcudnn.so", "libcuda.so"} {
			if strings.Contains(l, n+".") || strings.Contains(l, n+" ") {
				libs = append(libs, strings.Fields(l)[0])
			}
		}
	}
	return strings.Join(libs, ", ")
}

func amdVRAMUsedMB() (float64, bool) {
	ms, _ := os.ReadDir("/sys/class/drm")
	for _, m := range ms {
		b, err := os.ReadFile("/sys/class/drm/" + m.Name() + "/device/mem_info_vram_used")
		if err == nil {
			return float64(parseInt(strings.TrimSpace(string(b)))) / 1048576, true
		}
	}
	return 0, false
}
