package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func kbToMB(s string) float64 { return float64(parseInt(s)) / 1024 }

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// nvidiaSMI runs a read-only nvidia-smi query; it reads driver counters and
// starts no GPU work.
func nvidiaSMI(args ...string) (string, bool) {
	out, err := exec.Command("nvidia-smi", args...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// vramSampler polls GPU memory while a run is in progress. It reports this
// process's own usage (nvidia-smi compute-apps) when the driver lists it, and
// otherwise the growth of total used VRAM over the baseline, which is only
// meaningful on an otherwise idle GPU. Method says which one produced the
// number. On Windows and Intel iGPUs (shared system memory) it reports nothing.
type vramSampler struct {
	mu       sync.Mutex
	stop     chan struct{}
	done     chan struct{}
	baseline float64
	peakTot  float64
	peakOwn  float64
	have     bool
	amd      bool
}

func startVRAM(accel string) *vramSampler {
	v := &vramSampler{stop: make(chan struct{}), done: make(chan struct{})}
	if accel == "cpu" || accel == "directml" { // nothing to measure; do not poll any GPU driver
		close(v.done)
		return v
	}
	if s, ok := nvidiaSMI("--query-gpu=memory.used", "--format=csv,noheader,nounits"); ok {
		v.baseline = float64(parseInt(strings.Split(s, "\n")[0]))
		v.have = true
	} else if m, ok := amdVRAMUsedMB(); ok {
		v.baseline, v.have, v.amd = m, true, true
	}
	if !v.have {
		close(v.done)
		return v
	}
	go func() {
		defer close(v.done)
		pid := strconv.Itoa(os.Getpid())
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			v.sample(pid)
			select {
			case <-v.stop:
				v.sample(pid)
				return
			case <-t.C:
			}
		}
	}()
	return v
}

func (v *vramSampler) sample(pid string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.amd {
		if m, ok := amdVRAMUsedMB(); ok && m > v.peakTot {
			v.peakTot = m
		}
		return
	}
	if s, ok := nvidiaSMI("--query-gpu=memory.used", "--format=csv,noheader,nounits"); ok {
		if m := float64(parseInt(strings.Split(s, "\n")[0])); m > v.peakTot {
			v.peakTot = m
		}
	}
	if s, ok := nvidiaSMI("--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits"); ok {
		for _, l := range strings.Split(s, "\n") {
			f := strings.Split(l, ",")
			if len(f) == 2 && strings.TrimSpace(f[0]) == pid {
				if m := float64(parseInt(f[1])); m > v.peakOwn {
					v.peakOwn = m
				}
			}
		}
	}
}

// Stop returns peak VRAM in MB and how it was measured, or (0, "") if there is
// nothing to report.
func (v *vramSampler) Stop() (float64, string) {
	close(v.stop)
	<-v.done
	if !v.have {
		return 0, ""
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.peakOwn > 0 {
		return v.peakOwn, "own-pid"
	}
	if d := v.peakTot - v.baseline; d > 0 {
		return d, "delta-total"
	}
	return 0, "none-detected"
}

// Detection is printed by -detect and tells RUN.md / LAPTOP.md readers what the
// machine can do. Nothing here starts GPU work.
type Detection struct {
	OS, Arch     string
	CPU          string
	CPUFlags     []string `json:",omitempty"`
	NumCPU       int
	GPUs         []string `json:",omitempty"`
	NvidiaSMI    string   `json:",omitempty"`
	CUDALibs     string   `json:",omitempty"`
	VulkanLoader string   `json:",omitempty"`
	Engines      map[string][]string
	Suggested    []string
}

func detect() Detection {
	d := Detection{OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU(), Engines: map[string][]string{}}
	d.CPU, d.CPUFlags = cpuInfo()
	d.GPUs = gpuList()
	if s, ok := nvidiaSMI("--query-gpu=name,memory.total,driver_version", "--format=csv,noheader"); ok {
		d.NvidiaSMI = s
	}
	d.CUDALibs = cudaPresent()
	d.VulkanLoader = vulkanPresent()
	for n, e := range engines {
		d.Engines[n] = e.Accels
	}
	d.Suggested = []string{"cpu"}
	if d.NvidiaSMI != "" {
		d.Suggested = append(d.Suggested, "cuda")
	}
	if d.VulkanLoader != "" {
		d.Suggested = append(d.Suggested, "vulkan")
	}
	return d
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	os.Stdout.Write(append(b, '\n'))
}
