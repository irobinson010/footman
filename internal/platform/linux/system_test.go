//go:build linux

package linux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// testSys is a sysInfo on a copy of testdata/sysroot, with an environment,
// stub commands and sizes for the mounts.
func testSys(t *testing.T, env map[string]string, cmds map[string]string) *sysInfo {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("testdata/sysroot")); err != nil {
		t.Fatal(err)
	}
	s := newSysInfo(func(_ context.Context, r runSpec) (runResult, error) {
		out, ok := cmds[r.Name]
		if !ok {
			return runResult{}, errNoTool
		}
		return runResult{Stdout: out}, nil
	})
	s.root = root
	s.getenv = func(k string) string { return env[k] }
	s.statfs = func(path string) (uint64, uint64, error) {
		switch path {
		case "/":
			return 500 << 30, 100 << 30, nil
		case "/mnt/big disk":
			return 4 << 40, 1 << 40, nil
		}
		t.Errorf("statfs(%q): not a local disk", path)
		return 0, 0, os.ErrNotExist
	}
	// Between the two CPU samples, time passes in the fixture.
	s.wait = func(context.Context) error {
		for _, f := range []string{"proc/stat", "proc/42/stat"} {
			if err := os.Rename(filepath.Join(root, f+".after"), filepath.Join(root, f)); err != nil {
				return err
			}
		}
		return nil
	}
	return s
}

const lspciOut = `00:00.0 "Host bridge" "Advanced Micro Devices, Inc. [AMD]" "Device 14d8" -p00 "Example Mainboard Vendor" "Device 8877"
01:00.0 "VGA compatible controller" "NVIDIA Corporation" "AD103 [GeForce RTX 4060 Ti]" -ra1 -p00 "Example Mainboard Vendor" "Device 88a1"
01:00.1 "Audio device" "NVIDIA Corporation" "AD103 High Definition Audio Controller" -ra1 -p00 "Example Mainboard Vendor" "Device 88a1"
13:00.0 "VGA compatible controller" "Advanced Micro Devices, Inc. [AMD/ATI]" "Raphael" -rc2 -p00 "Example Mainboard Vendor" "Device 8877"
`

func TestFacts(t *testing.T) {
	s := testSys(t, map[string]string{
		"XDG_CURRENT_DESKTOP": "KDE", "XDG_SESSION_TYPE": "wayland",
		"SHELL": "/usr/bin/zsh", "LANG": "en_US.UTF-8",
		"USER": "secret-user", "HOSTNAME": "secret-host",
	}, map[string]string{"lspci": lspciOut})
	f := s.facts(t.Context())
	want := platform.SystemFacts{
		OS: "Linux", Distro: "Ubuntu 24.04.1 LTS", Kernel: "6.8.0-1-generic",
		Desktop: "KDE", Session: "wayland", Shell: "zsh",
		CPU: "AMD Ryzen 9 5900X 12-Core Processor", Cores: 2, Threads: 4,
		MemoryBytes: 65536000 * 1024,
		GPUs:        []string{"NVIDIA GeForce RTX 4060 Ti", "AMD Raphael"},
		Locale:      "en_US.UTF-8", TimeZone: "Europe/Berlin", Arch: f.Arch,
	}
	if f.Arch == "" {
		t.Error("no Arch")
	}
	if !factsEqual(f, want) {
		t.Errorf("facts =\n%+v\nwant\n%+v", f, want)
	}
	// Cached: lspci is not run again, and changes are not seen.
	s.run = nil
	if g := s.facts(t.Context()); !factsEqual(g, f) {
		t.Errorf("second Facts = %+v", g)
	}
}

func factsEqual(a, b platform.SystemFacts) bool { return reflect.DeepEqual(a, b) }

// Without lspci the NVIDIA driver names its cards; without the session's
// environment, the shell comes from passwd (here only root's, so none) and
// the session type from WAYLAND_DISPLAY or DISPLAY.
func TestFactsFallbacks(t *testing.T) {
	s := testSys(t, map[string]string{"DISPLAY": ":0", "TZ": ":America/Chicago", "LC_ALL": "C", "LANG": "de_DE.UTF-8"}, nil)
	f := s.facts(t.Context())
	if !slices.Equal(f.GPUs, []string{"NVIDIA GeForce RTX 4060 Ti"}) {
		t.Errorf("GPUs = %q", f.GPUs)
	}
	if f.Shell != "" && os.Getuid() != 0 {
		t.Errorf("shell = %q, want none (passwd has only root)", f.Shell)
	}
	if f.Session != "x11" || f.Desktop != "" {
		t.Errorf("desktop, session = %q, %q", f.Desktop, f.Session)
	}
	if f.TimeZone != "America/Chicago" || f.Locale != "de_DE.UTF-8" {
		t.Errorf("time zone, locale = %q, %q", f.TimeZone, f.Locale)
	}
}

func TestShellFromPasswd(t *testing.T) {
	s := testSys(t, nil, nil)
	line := fmt.Sprintf("me:x:%d:%d::/home/me:/usr/bin/fish\n", os.Getuid(), os.Getgid())
	if err := os.WriteFile(filepath.Join(s.root, "etc/passwd"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.shell(); got != "fish" {
		t.Errorf("shell = %q", got)
	}
}

func TestStatus(t *testing.T) {
	s := testSys(t, nil, map[string]string{
		"nvidia-smi": "NVIDIA GeForce RTX 4060 Ti, 2048, 16376, 7, 41\n",
	})
	st, err := s.status(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	page := uint64(os.Getpagesize())
	names := func(ps []platform.Process) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	// Kernel threads (no memory, no CPU time) are left out.
	if got := names(st.TopMemory); !slices.Equal(got, []string{"Web Content (x)", "Web Content (x)", "systemd"}) {
		t.Errorf("top memory = %q", got)
	}
	if p := st.TopMemory[0]; p.PID != 42 || p.RSSBytes != 786432*page {
		t.Errorf("top process = %+v", p)
	}
	if len(st.TopMemoryByName) != 1 || st.TopMemoryByName[0].Count != 2 || st.TopMemoryByName[0].RSSBytes != (786432+262144)*page {
		t.Errorf("by name = %+v", st.TopMemoryByName)
	}
	// pid 42 used 300 of the 1000 jiffies that passed on 4 CPUs: 120% of
	// one CPU. The idle ones are left out.
	if len(st.TopCPU) != 1 || st.TopCPU[0].PID != 42 || st.TopCPU[0].CPU < 119.9 || st.TopCPU[0].CPU > 120.1 {
		t.Errorf("top CPU = %+v", st.TopCPU)
	}
	// 600 of 1000 jiffies were idle or waiting.
	if st.CPUBusy < 39.9 || st.CPUBusy > 40.1 {
		t.Errorf("CPU busy = %v", st.CPUBusy)
	}
	if st.Memory != (platform.Memory{TotalBytes: 65536000 * 1024, AvailableBytes: 40960000 * 1024, SwapTotalBytes: 8388608 * 1024, SwapFreeBytes: 4194304 * 1024}) {
		t.Errorf("memory = %+v", st.Memory)
	}
	if st.Load != [3]float64{1.5, 1, 0.5} || st.Uptime != 93784*time.Second {
		t.Errorf("load, uptime = %v, %v", st.Load, st.Uptime)
	}
	// Local disks once per device, not the EFI partition, snaps, tmpfs or
	// network mounts.
	wantDisks := []platform.Disk{
		{Mount: "/", FSType: "ext4", TotalBytes: 500 << 30, FreeBytes: 100 << 30},
		{Mount: "/mnt/big disk", FSType: "btrfs", TotalBytes: 4 << 40, FreeBytes: 1 << 40},
	}
	if !slices.Equal(st.Disks, wantDisks) {
		t.Errorf("disks = %+v", st.Disks)
	}
	if !slices.Equal(st.Batteries, []platform.Battery{{Name: "BAT0", Percent: 81, Status: "Discharging"}}) {
		t.Errorf("batteries = %+v", st.Batteries)
	}
	if !slices.Equal(st.GPUs, []platform.GPUStatus{{Name: "NVIDIA GeForce RTX 4060 Ti", MemUsedBytes: 2048 << 20, MemTotalBytes: 16376 << 20, UtilizationPercent: 7, TemperatureC: 41}}) {
		t.Errorf("GPUs = %+v", st.GPUs)
	}
}

func TestStatusTop(t *testing.T) {
	s := testSys(t, nil, nil)
	st, err := s.status(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.TopMemory) != 1 || st.GPUs != nil {
		t.Errorf("top 1 = %+v, GPUs %+v (no nvidia-smi)", st.TopMemory, st.GPUs)
	}
}

func TestStatusNoProc(t *testing.T) {
	s := newSysInfo(nil)
	s.root = t.TempDir()
	if _, err := s.status(t.Context(), 10); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("err = %v", err)
	}
}

func TestParseProcStat(t *testing.T) {
	for _, c := range []struct {
		line string
		ok   bool
		name string
	}{
		{"7 (a) b) R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21", true, "a) b"},
		{"7 (short) R 1 2", false, ""},
		{"garbage", false, ""},
		{"x (n) R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21", false, ""},
	} {
		ps, ok := parseProcStat(c.line, 4096)
		if ok != c.ok || ps.name != c.name {
			t.Errorf("parseProcStat(%q) = %+v, %v", c.line, ps, ok)
		}
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/a\040b\134c\0`); got != `/a b\c\0` {
		t.Errorf("unescapeMount = %q", got)
	}
}

// A sanity check on the real /proc: it must not fail, and must report this
// process. It reads files only (no desktop session).
func TestStatusRealProc(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	s := newSysInfo(nil)
	s.wait = func(context.Context) error { return nil }
	st, err := s.status(t.Context(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if st.Memory.TotalBytes == 0 || len(st.TopMemory) == 0 {
		t.Errorf("status = %+v", st)
	}
	found := false
	for _, p := range st.TopMemory {
		found = found || p.PID == os.Getpid()
	}
	if !found {
		t.Error("this process is not listed")
	}
}
