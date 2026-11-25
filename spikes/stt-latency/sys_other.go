//go:build !linux && !windows

package main

func rssMB() (cur, peak float64)     { return 0, 0 }
func cpuInfo() (string, []string)    { return "", nil }
func gpuList() []string              { return nil }
func vulkanPresent() string          { return "" }
func cudaPresent() string            { return "" }
func amdVRAMUsedMB() (float64, bool) { return 0, false }
