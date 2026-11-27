package platform

import (
	"context"
	"os"
	"runtime"
	"strings"
	"time"
)

// SystemFacts is coarse, slow-changing information about the computer: what
// the AI is told so it answers for this machine rather than for every
// operating system (context item "system", ADR-0043). It holds no host name,
// user name, paths or other personal content. Empty fields are unknown.
type SystemFacts struct {
	// OS is the operating system family, such as "Linux".
	OS string
	// Distro is the distribution or edition and version, such as
	// "Ubuntu 25.10".
	Distro string
	// Kernel is the kernel version, such as "6.17.0-5-generic".
	Kernel string
	// Desktop is the desktop environment, such as "KDE".
	Desktop string
	// Session is the display session type, such as "wayland" or "x11".
	Session string
	// Shell is the user's login shell by name, such as "bash".
	Shell string
	// CPU is the processor model.
	CPU string
	// Cores and Threads are the physical cores and logical CPUs (0 if
	// unknown).
	Cores, Threads int
	// MemoryBytes is the installed RAM.
	MemoryBytes uint64
	// GPUs are the graphics adapters by name.
	GPUs []string
	// Locale is the user's locale, such as "en_US.UTF-8".
	Locale string
	// TimeZone is the IANA time zone, such as "America/Chicago".
	TimeZone string
	// Arch is the platform Footman runs on, such as "linux/amd64".
	Arch string
}

// Process is one running process, by name only: command lines can hold
// secrets and are never reported.
type Process struct {
	// Name is the short process name (on Linux, comm).
	Name string
	// PID is the process ID.
	PID int
	// RSSBytes is its resident memory.
	RSSBytes uint64
	// CPU is its CPU use over the sample, in percent of one CPU (so it can
	// exceed 100 on several cores).
	CPU float64
	// Count is, in SystemStatus.TopMemoryByName, how many processes share
	// the name (PID is then 0); otherwise 0.
	Count int
}

// Memory is RAM and swap use.
type Memory struct {
	TotalBytes, AvailableBytes    uint64
	SwapTotalBytes, SwapFreeBytes uint64
}

// Disk is one mounted local filesystem.
type Disk struct {
	// Mount is where it is mounted, such as "/" or "/home".
	Mount string
	// FSType is the filesystem type, such as "ext4".
	FSType string
	// TotalBytes and FreeBytes are its size and the space free to the user.
	TotalBytes, FreeBytes uint64
}

// Battery is one battery.
type Battery struct {
	// Name identifies it, such as "BAT0".
	Name string
	// Percent is its charge (0 to 100).
	Percent int
	// Status is "Charging", "Discharging", "Full" or similar.
	Status string
}

// GPUStatus is one graphics card's memory and load, where the driver tells.
type GPUStatus struct {
	Name                        string
	MemUsedBytes, MemTotalBytes uint64
	UtilizationPercent          int
	TemperatureC                int
}

// SystemStatus is what the computer is doing right now.
type SystemStatus struct {
	// TopMemory are the processes using the most memory, most first.
	TopMemory []Process
	// TopMemoryByName sums memory over processes with the same name (a
	// browser's many processes), most first.
	TopMemoryByName []Process
	// TopCPU are the processes using the most CPU over the sample, most
	// first.
	TopCPU []Process
	// CPUBusy is the share of all CPUs in use over the sample, in percent.
	CPUBusy float64
	// Memory is RAM and swap use.
	Memory Memory
	// Load is the 1, 5 and 15 minute load average (zero where there is
	// none).
	Load [3]float64
	// Uptime is how long since the computer started.
	Uptime time.Duration
	// Disks are the mounted local filesystems.
	Disks []Disk
	// Batteries are present batteries; none on most desktops.
	Batteries []Battery
	// GPUs are graphics cards whose driver reports use.
	GPUs []GPUStatus
}

// SystemInfo is an optional extension of Platform for looking at the
// computer itself. Read-only: nothing here changes the system. The AI's
// system_status tool and the "system" context item check for it with a type
// assertion; a platform without it reports BasicFacts and offers no tool.
type SystemInfo interface {
	// Facts returns the machine's coarse facts. Implementations collect
	// them once and cache them: it is called when the daemon starts.
	Facts(ctx context.Context) (SystemFacts, error)
	// Status samples what the computer is doing now, with the top processes
	// by memory and by CPU (at most top of each).
	Status(ctx context.Context, top int) (SystemStatus, error)
}

// BasicFacts are the facts any platform knows from the Go runtime and the
// environment: the OS family, platform, logical CPUs, shell, locale and time
// zone.
func BasicFacts() SystemFacts {
	f := SystemFacts{
		OS:       osName(runtime.GOOS),
		Arch:     runtime.GOOS + "/" + runtime.GOARCH,
		Threads:  runtime.NumCPU(),
		Locale:   Locale(),
		TimeZone: os.Getenv("TZ"),
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		f.Shell = sh[strings.LastIndexAny(sh, `/\`)+1:]
	}
	if f.TimeZone == "" && time.Local.String() != "Local" {
		f.TimeZone = time.Local.String()
	}
	return f
}

// Locale is the user's locale from LC_ALL, LC_MESSAGES or LANG, or "".
func Locale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return v
		}
	}
	return ""
}

func osName(goos string) string {
	switch goos {
	case "linux":
		return "Linux"
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "freebsd":
		return "FreeBSD"
	}
	return goos
}
