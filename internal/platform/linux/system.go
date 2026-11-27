//go:build linux

package linux

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// The system facts and status (platform.SystemInfo) come from /proc, /sys,
// /etc/os-release and the environment, read directly: no ps, top or free.
// Facts run lspci once (cached); Status runs nvidia-smi only when it is
// installed. Process command lines are never read, only names (comm).

var _ platform.SystemInfo = (*Platform)(nil)

// Facts implements platform.SystemInfo. The facts are collected on the
// first call and cached.
func (p *Platform) Facts(ctx context.Context) (platform.SystemFacts, error) {
	return p.sys.facts(ctx), nil
}

// Status implements platform.SystemInfo.
func (p *Platform) Status(ctx context.Context, top int) (platform.SystemStatus, error) {
	return p.sys.status(ctx, top)
}

// sysInfo reads the system from a filesystem root ("/" except in tests).
type sysInfo struct {
	root   string
	run    runFunc
	getenv func(string) string
	// wait is the pause between Status's two CPU samples (sleep for
	// sampleTime by default).
	wait func(ctx context.Context) error
	// statfs reports a mount's size; syscall.Statfs by default.
	statfs func(path string) (total, free uint64, err error)

	once   sync.Once
	cached platform.SystemFacts
}

func newSysInfo(run runFunc) *sysInfo {
	return &sysInfo{root: "/", run: run, getenv: os.Getenv, wait: sleepSample, statfs: statfs}
}

// sampleTime is how long Status measures CPU use over.
const sampleTime = 500 * time.Millisecond

func sleepSample(ctx context.Context) error {
	t := time.NewTimer(sampleTime)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *sysInfo) path(p string) string { return filepath.Join(s.root, p) }

func (s *sysInfo) read(p string) string {
	b, err := os.ReadFile(s.path(p))
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *sysInfo) facts(ctx context.Context) platform.SystemFacts {
	s.once.Do(func() { s.cached = s.collectFacts(ctx) })
	f := s.cached
	f.GPUs = slices.Clone(f.GPUs)
	return f
}

func (s *sysInfo) collectFacts(ctx context.Context) platform.SystemFacts {
	f := platform.SystemFacts{OS: "Linux", Arch: runtime.GOOS + "/" + runtime.GOARCH}
	osr := parseKeyValues(s.read("etc/os-release"))
	f.Distro = osr["PRETTY_NAME"]
	if f.Distro == "" {
		f.Distro = strings.TrimSpace(osr["NAME"] + " " + osr["VERSION_ID"])
	}
	f.Kernel = strings.TrimSpace(s.read("proc/sys/kernel/osrelease"))
	f.Desktop, f.Session = s.desktop()
	f.Shell = s.shell()
	f.CPU, f.Cores, f.Threads = parseCPUInfo(s.read("proc/cpuinfo"))
	if f.Threads == 0 {
		f.Threads = runtime.NumCPU()
	}
	f.MemoryBytes = parseMeminfo(s.read("proc/meminfo"))["MemTotal"]
	f.GPUs = s.gpus(ctx)
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := s.getenv(k); v != "" && v != "C" && v != "POSIX" {
			f.Locale = v
			break
		}
	}
	f.TimeZone = s.timeZone()
	return f
}

// desktop names the desktop environment and session type from the session's
// environment (which the systemd user manager has on desktops that import
// it, such as Plasma and GNOME).
func (s *sysInfo) desktop() (desktop, session string) {
	desktop = s.getenv("XDG_CURRENT_DESKTOP")
	if desktop == "" {
		desktop = s.getenv("XDG_SESSION_DESKTOP")
	}
	if desktop == "" && s.getenv("KDE_FULL_SESSION") != "" {
		desktop = "KDE"
	}
	desktop = strings.ReplaceAll(desktop, ":", ", ")
	session = s.getenv("XDG_SESSION_TYPE")
	switch {
	case session != "" && session != "tty":
	case s.getenv("WAYLAND_DISPLAY") != "":
		session = "wayland"
	case s.getenv("DISPLAY") != "":
		session = "x11"
	}
	return desktop, session
}

// shell is the login shell's name: $SHELL, else the user's /etc/passwd
// entry.
func (s *sysInfo) shell() string {
	sh := s.getenv("SHELL")
	if sh == "" {
		uid := strconv.Itoa(os.Getuid())
		for line := range strings.SplitSeq(s.read("etc/passwd"), "\n") {
			if f := strings.Split(line, ":"); len(f) >= 7 && f[2] == uid {
				sh = f[6]
				break
			}
		}
	}
	if sh == "" {
		return ""
	}
	return filepath.Base(sh)
}

// timeZone is $TZ, else where /etc/localtime points, else /etc/timezone.
func (s *sysInfo) timeZone() string {
	if tz := strings.TrimPrefix(s.getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if l, err := os.Readlink(s.path("etc/localtime")); err == nil {
		if _, after, ok := strings.Cut(l, "zoneinfo/"); ok {
			return after
		}
	}
	return strings.TrimSpace(s.read("etc/timezone"))
}

// gpus names the graphics adapters: from lspci when installed, else the
// NVIDIA driver's /proc entries.
func (s *sysInfo) gpus(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if s.run != nil {
		if r, err := s.run(ctx, runSpec{Name: "lspci", Args: []string{"-mm"}, Capture: true}); err == nil {
			if g := parseLspci(r.Stdout); len(g) > 0 {
				return g
			}
		}
	}
	var out []string
	dirs, _ := filepath.Glob(s.path("proc/driver/nvidia/gpus/*/information"))
	for _, d := range dirs {
		kv := parseColonValues(s.read(strings.TrimPrefix(d, s.root)))
		if m := kv["Model"]; m != "" {
			out = append(out, m)
		}
	}
	return out
}

// parseLspci picks the display controllers from `lspci -mm` output and
// names each by short vendor and model: "NVIDIA GeForce RTX 4060 Ti".
func parseLspci(out string) []string {
	var gpus []string
	for line := range strings.SplitSeq(out, "\n") {
		f := quotedFields(line)
		if len(f) < 4 {
			continue
		}
		class := strings.ToLower(f[1])
		if !strings.Contains(class, "vga") && !strings.Contains(class, "3d") && !strings.Contains(class, "display") {
			continue
		}
		vendor, model := shortVendor(f[2]), f[3]
		if i, j := strings.Index(model, "["), strings.LastIndex(model, "]"); i >= 0 && j > i {
			model = model[i+1 : j]
		}
		name := strings.TrimSpace(vendor + " " + model)
		if !strings.HasPrefix(strings.ToLower(model), strings.ToLower(vendor)) {
			gpus = append(gpus, name)
		} else {
			gpus = append(gpus, model)
		}
	}
	return gpus
}

// quotedFields splits an `lspci -mm` line into its slot and quoted fields,
// skipping the unquoted -r and -p options.
func quotedFields(line string) []string {
	slot, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
	if !ok {
		return nil
	}
	out := []string{slot}
	for {
		i := strings.IndexByte(rest, '"')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(rest[i+1:], '"')
		if j < 0 {
			return out
		}
		out = append(out, rest[i+1:i+1+j])
		rest = rest[i+j+2:]
	}
}

func shortVendor(v string) string {
	l := strings.ToLower(v)
	switch {
	case strings.Contains(l, "nvidia"):
		return "NVIDIA"
	case strings.Contains(l, "advanced micro devices"), strings.Contains(l, "amd/ati"):
		return "AMD"
	case strings.Contains(l, "intel"):
		return "Intel"
	}
	for _, suf := range []string{" Corporation", " Corp.", ", Inc.", " Inc.", " Ltd."} {
		v = strings.TrimSuffix(v, suf)
	}
	return v
}

// parseKeyValues parses os-release style KEY="value" lines.
func parseKeyValues(s string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `'"`)
		}
		out[k] = v
	}
	return out
}

// parseColonValues parses "Key: value" lines (with tabs and spaces).
func parseColonValues(s string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// parseCPUInfo returns the first model name, the physical cores (distinct
// physical id and core id pairs) and the logical CPUs.
func parseCPUInfo(s string) (model string, cores, threads int) {
	type core struct{ pkg, id string }
	seen := map[core]bool{}
	var cur core
	flush := func() {
		if cur.id != "" {
			seen[cur] = true
		}
		cur = core{}
	}
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			if strings.TrimSpace(line) == "" {
				flush()
			}
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			threads++
		case "model name", "Model", "cpu model":
			if model == "" {
				model = v
			}
		case "physical id":
			cur.pkg = v
		case "core id":
			cur.id = v
		}
	}
	flush()
	return model, len(seen), threads
}

// parseMeminfo returns /proc/meminfo's values in bytes.
func parseMeminfo(s string) map[string]uint64 {
	out := map[string]uint64{}
	for line := range strings.SplitSeq(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

// procStat is what Status needs from /proc/<pid>/stat.
type procStat struct {
	pid   int
	name  string
	ticks uint64 // utime + stime
	rss   uint64 // bytes
}

// parseProcStat parses one /proc/<pid>/stat line. The name is in
// parentheses and may itself contain spaces and parentheses.
func parseProcStat(s string, pageSize uint64) (procStat, bool) {
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return procStat{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return procStat{}, false
	}
	// Fields after the name start at field 3 (state).
	f := strings.Fields(s[closing+1:])
	if len(f) < 22 {
		return procStat{}, false
	}
	utime, _ := strconv.ParseUint(f[11], 10, 64)
	stime, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ := strconv.ParseInt(f[21], 10, 64)
	if rss < 0 {
		rss = 0
	}
	return procStat{pid: pid, name: s[open+1 : closing], ticks: utime + stime, rss: uint64(rss) * pageSize}, true
}

// parseCPUTotal returns the total and idle jiffies of all CPUs from /proc/stat.
func parseCPUTotal(s string) (total, idle uint64) {
	for line := range strings.SplitSeq(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		for i, x := range f[1:] {
			n, _ := strconv.ParseUint(x, 10, 64)
			// guest and guest_nice (9, 10) are already in user and nice.
			if i < 8 {
				total += n
			}
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		return total, idle
	}
	return 0, 0
}

// processes reads every process's stat.
func (s *sysInfo) processes() map[int]procStat {
	out := map[int]procStat{}
	ents, err := os.ReadDir(s.path("proc"))
	if err != nil {
		return out
	}
	page := uint64(os.Getpagesize())
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.path("proc"), e.Name(), "stat"))
		if err != nil {
			continue // gone, or not ours to read
		}
		if ps, ok := parseProcStat(string(b), page); ok {
			out[ps.pid] = ps
		}
	}
	return out
}

func (s *sysInfo) status(ctx context.Context, top int) (platform.SystemStatus, error) {
	var st platform.SystemStatus
	t0, idle0 := parseCPUTotal(s.read("proc/stat"))
	before := s.processes()
	if len(before) == 0 {
		return st, fmt.Errorf("linux system status: %w: cannot read %s", platform.ErrUnsupported, s.path("proc"))
	}
	if err := s.wait(ctx); err != nil {
		return st, err
	}
	stat := s.read("proc/stat")
	t1, idle1 := parseCPUTotal(stat)
	after := s.processes()

	var procs []platform.Process
	ncpu := float64(max(1, strings.Count(stat, "\ncpu")))
	dt := float64(t1 - t0)
	for pid, a := range after {
		if a.rss == 0 && a.ticks == 0 {
			continue // kernel threads
		}
		p := platform.Process{Name: a.name, PID: pid, RSSBytes: a.rss}
		if b, ok := before[pid]; ok && dt > 0 && a.ticks >= b.ticks {
			// Jiffies of the process over jiffies of all CPUs, scaled to
			// percent of one CPU, like top.
			p.CPU = float64(a.ticks-b.ticks) / dt * ncpu * 100
		}
		procs = append(procs, p)
	}
	if dt > 0 && t1 > t0 {
		st.CPUBusy = (1 - float64(idle1-idle0)/dt) * 100
	}
	st.TopMemory = topBy(procs, top, func(p platform.Process) float64 { return float64(p.RSSBytes) })
	st.TopMemoryByName = topBy(byName(procs), min(top, 5), func(p platform.Process) float64 { return float64(p.RSSBytes) })
	st.TopCPU = topBy(procs, top, func(p platform.Process) float64 { return p.CPU })
	st.TopCPU = slices.DeleteFunc(st.TopCPU, func(p platform.Process) bool { return p.CPU < 0.05 })

	mi := parseMeminfo(s.read("proc/meminfo"))
	st.Memory = platform.Memory{
		TotalBytes: mi["MemTotal"], AvailableBytes: mi["MemAvailable"],
		SwapTotalBytes: mi["SwapTotal"], SwapFreeBytes: mi["SwapFree"],
	}
	if f := strings.Fields(s.read("proc/loadavg")); len(f) >= 3 {
		for i := range 3 {
			st.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if f := strings.Fields(s.read("proc/uptime")); len(f) >= 1 {
		if sec, err := strconv.ParseFloat(f[0], 64); err == nil {
			st.Uptime = time.Duration(sec) * time.Second
		}
	}
	st.Disks = s.disks()
	st.Batteries = s.batteries()
	st.GPUs = s.gpuStatus(ctx)
	return st, nil
}

// byName sums processes by name, keeping names shared by more than one.
func byName(ps []platform.Process) []platform.Process {
	sum := map[string]*platform.Process{}
	for _, p := range ps {
		g, ok := sum[p.Name]
		if !ok {
			g = &platform.Process{Name: p.Name}
			sum[p.Name] = g
		}
		g.RSSBytes += p.RSSBytes
		g.CPU += p.CPU
		g.Count++
	}
	var out []platform.Process
	for _, g := range sum {
		if g.Count > 1 {
			out = append(out, *g)
		}
	}
	return out
}

// topBy returns the n processes with the largest key, largest first (ties
// by PID, for stable output).
func topBy(ps []platform.Process, n int, key func(platform.Process) float64) []platform.Process {
	out := slices.Clone(ps)
	slices.SortFunc(out, func(a, b platform.Process) int {
		ka, kb := key(a), key(b)
		switch {
		case ka > kb:
			return -1
		case ka < kb:
			return 1
		}
		if a.PID != b.PID {
			return a.PID - b.PID
		}
		return strings.Compare(a.Name, b.Name)
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// localFS are the filesystem types disks reports. Network filesystems are
// left out: statfs on a dead server can hang.
var localFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "f2fs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true, "zfs": true,
	"jfs": true, "reiserfs": true, "bcachefs": true, "hfsplus": true,
}

// disks reports the local filesystems in /proc/self/mounts, one per device
// (btrfs subvolumes share theirs).
func (s *sysInfo) disks() []platform.Disk {
	var out []platform.Disk
	seen := map[string]bool{}
	for line := range strings.SplitSeq(s.read("proc/self/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !localFS[f[2]] || seen[f[0]] {
			continue
		}
		mount := unescapeMount(f[1])
		if strings.HasPrefix(mount, "/snap/") || strings.HasPrefix(mount, "/boot/efi") {
			continue
		}
		total, free, err := s.statfs(mount)
		if err != nil || total == 0 {
			continue
		}
		seen[f[0]] = true
		out = append(out, platform.Disk{Mount: mount, FSType: f[2], TotalBytes: total, FreeBytes: free})
	}
	return out
}

// unescapeMount undoes /proc/mounts' octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func statfs(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, nil
}

// batteries reads /sys/class/power_supply.
func (s *sysInfo) batteries() []platform.Battery {
	var out []platform.Battery
	dirs, _ := filepath.Glob(s.path("sys/class/power_supply/*"))
	for _, d := range dirs {
		rel := strings.TrimPrefix(d, s.root)
		if strings.TrimSpace(s.read(filepath.Join(rel, "type"))) != "Battery" {
			continue
		}
		if p := strings.TrimSpace(s.read(filepath.Join(rel, "present"))); p == "0" {
			continue
		}
		pct, err := strconv.Atoi(strings.TrimSpace(s.read(filepath.Join(rel, "capacity"))))
		if err != nil {
			continue
		}
		out = append(out, platform.Battery{Name: filepath.Base(d), Percent: pct, Status: strings.TrimSpace(s.read(filepath.Join(rel, "status")))})
	}
	return out
}

// gpuStatus asks nvidia-smi, when installed, for each card's memory, load
// and temperature.
func (s *sysInfo) gpuStatus(ctx context.Context) []platform.GPUStatus {
	if s.run == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := s.run(ctx, runSpec{Name: "nvidia-smi", Capture: true, Args: []string{
		"--query-gpu=name,memory.used,memory.total,utilization.gpu,temperature.gpu",
		"--format=csv,noheader,nounits",
	}})
	if err != nil {
		return nil
	}
	return parseNvidiaSMI(r.Stdout)
}

// parseNvidiaSMI parses nvidia-smi's CSV (memory in MiB).
func parseNvidiaSMI(out string) []platform.GPUStatus {
	var gs []platform.GPUStatus
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 5 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		used, _ := strconv.ParseUint(f[1], 10, 64)
		total, _ := strconv.ParseUint(f[2], 10, 64)
		util, _ := strconv.Atoi(f[3])
		temp, _ := strconv.Atoi(f[4])
		gs = append(gs, platform.GPUStatus{Name: f[0], MemUsedBytes: used << 20, MemTotalBytes: total << 20, UtilizationPercent: util, TemperatureC: temp})
	}
	return gs
}
