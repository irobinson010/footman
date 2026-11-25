//go:build !(native && cgo && linux)

// Package memtrim gives memory freed by the native speech engines back to the
// operating system (see trim_glibc.go). This build has no native engines, so
// Trim does nothing.
package memtrim

// Trim does nothing in this build.
func Trim() {}
