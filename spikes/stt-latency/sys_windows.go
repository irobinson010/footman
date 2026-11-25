//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func rssMB() (cur, peak float64) {
	k := syscall.NewLazyDLL("kernel32.dll")
	proc := k.NewProc("K32GetProcessMemoryInfo")
	h, _ := syscall.GetCurrentProcess()
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	r, _, _ := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb))
	if r == 0 {
		return 0, 0
	}
	return float64(c.WorkingSetSize) / 1048576, float64(c.PeakWorkingSetSize) / 1048576
}

func powershell(cmd string) string {
	out, err := exec.Command("powershell", "-NoProfile", "-Command", cmd).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func cpuInfo() (string, []string) {
	n := powershell("(Get-CimInstance Win32_Processor).Name")
	if n == "" {
		n = os.Getenv("PROCESSOR_IDENTIFIER")
	}
	return n, nil
}

func gpuList() []string {
	s := powershell("(Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name + ' (' + $_.DriverVersion + ')' }) -join \"`n\"")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func vulkanPresent() string {
	if _, err := os.Stat(os.Getenv("SystemRoot") + `\System32\vulkan-1.dll`); err == nil {
		return "vulkan-1.dll"
	}
	return ""
}

func cudaPresent() string { return "" }

func amdVRAMUsedMB() (float64, bool) { return 0, false }
