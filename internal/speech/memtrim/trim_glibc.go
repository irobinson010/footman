//go:build native && cgo && linux

// Package memtrim gives memory freed by the native speech engines back to the
// operating system. whisper.cpp and onnxruntime allocate their model and
// working buffers with malloc; when a model is unloaded (speech.unload_after,
// ADR-0028) glibc keeps the freed pages in its arenas, so the daemon's
// resident memory would not go down (ADR-0030). Trim asks glibc to release
// them. Elsewhere it does nothing.
package memtrim

/*
#include <stdlib.h>
#ifdef __GLIBC__
#include <malloc.h>
static void footman_trim(void) { malloc_trim(0); }
#else
static void footman_trim(void) {}
#endif
*/
import "C"

// Trim returns free heap memory to the operating system.
func Trim() { C.footman_trim() }
