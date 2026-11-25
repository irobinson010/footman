//go:build native

package main

// The native speech engines (cgo; ADR-0024, ADR-0027). They register in
// speech.STTs, speech.VADs and accel, so speech.stt.engine = "auto" can pick
// them. Build with `make native`.
import (
	_ "github.com/irobinson010/footman/internal/speech/sherpa"
	_ "github.com/irobinson010/footman/internal/speech/whispercpp"
)
