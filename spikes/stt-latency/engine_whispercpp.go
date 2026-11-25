//go:build whispercpp

package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
)

// The whisper.cpp Go bindings are a thin cgo layer over libwhisper. They do
// NOT ship the C library: scripts/build-whispercpp.sh builds it (cmake) for the
// chosen accelerator and prints the CGO_* environment to use. The accelerator
// is fixed at library build time, so each GPU backend is its own binary,
// labelled by the wcuda / wvulkan build tags below.

func init() {
	engines["whispercpp"] = engineEntry{
		New:    func() Engine { return &whisperEngine{} },
		Accels: []string{compiledAccel},
		Cgo:    true,
	}
}

type whisperEngine struct {
	model whisper.Model
	ctx   whisper.Context
	info  string
	bytes int64
}

func (e *whisperEngine) ModelBytes() int64 { return e.bytes }

func (e *whisperEngine) Info() string { return e.info }

func (e *whisperEngine) Load(o LoadOpts) error {
	if o.Model.GGML == "" {
		return fmt.Errorf("model %s has no ggml build", o.Model.ID)
	}
	if o.Accel != compiledAccel {
		return fmt.Errorf("this binary's whisper.cpp was built for %q, not %q (build another with scripts/build.sh whispercpp-ACCEL)", compiledAccel, o.Accel)
	}
	path := filepath.Join(o.ModelsDir, "whispercpp", o.Model.GGML)
	e.bytes = fileBytes(path)
	m, err := whisper.New(path)
	if err != nil {
		return err
	}
	ctx, err := m.NewContext()
	if err != nil {
		m.Close()
		return err
	}
	if m.IsMultilingual() { // English-only models reject SetLanguage
		if err := ctx.SetLanguage("en"); err != nil {
			m.Close()
			return err
		}
	}
	ctx.SetThreads(uint(o.Threads))
	e.model, e.ctx = m, ctx
	e.info = "whisper.cpp (bindings/go), build accel " + compiledAccel + "; " + strings.TrimSpace(ctx.SystemInfo())
	return nil
}

func (e *whisperEngine) Transcribe(c *Clip) (Result, error) {
	t := time.Now()
	if err := e.ctx.Process(c.Samples, nil, nil, nil); err != nil {
		return Result{}, err
	}
	inner := time.Since(t)
	var parts []string
	for {
		seg, err := e.ctx.NextSegment()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, err
		}
		parts = append(parts, strings.TrimSpace(seg.Text))
	}
	return Result{Text: strings.Join(parts, " "), Inner: inner}, nil
}

func (e *whisperEngine) Close() {
	if e.model != nil {
		e.model.Close()
		e.model = nil
	}
}
