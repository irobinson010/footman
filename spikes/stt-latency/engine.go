package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Clip is one test utterance: 16 kHz mono float32 samples (ADR-0021) plus the
// file it came from, for engines that shell out.
type Clip struct {
	ID      string
	Path    string
	Samples []float32
	Ref     string
}

func (c *Clip) Seconds() float64 { return float64(len(c.Samples)) / 16000 }

// LoadOpts is everything an engine needs to load one model.
type LoadOpts struct {
	Model     ModelSpec
	ModelsDir string
	Accel     string // cpu | cuda | vulkan | directml ...
	Threads   int
	Variant   string // sherpa only: int8 | fp32 | auto
	CLIPath   string // whispercli only: path to whisper-cli
}

// Result is what one Transcribe call produced.
type Result struct {
	Text string
	// Inner is the time spent in the engine's core decode call when it can be
	// separated from the Go wrapper work around it (stream setup, copying the
	// samples, reading the text back). Zero when not separable.
	Inner time.Duration
	// Latency, when non-zero, overrides the harness's own wall-clock timing.
	// Only the external-process engine uses it, to take the model load that
	// each process start repeats out of the measurement.
	Latency time.Duration
}

// Engine is the one small interface both speech engines sit behind. It is
// offline (whole clip in, final text out): the latency the spike cares about is
// the time from the end of the audio to the final text, which for an offline
// engine fed a complete clip is exactly the duration of Transcribe.
type Engine interface {
	Load(LoadOpts) error
	Transcribe(*Clip) (Result, error)
	Close()
	// Info describes the backend that actually loaded (library versions, the
	// accelerators it reports), for the results file.
	Info() string
	// ModelBytes is the size on disk of the weight files this engine loaded.
	ModelBytes() int64
}

func fileBytes(paths ...string) int64 {
	var n int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

type engineEntry struct {
	New    func() Engine
	Accels []string // accelerators compiled into (or supported by) this build
	Cgo    bool
}

// engines is filled in by init() functions in the build-tagged engine files.
var engines = map[string]engineEntry{}

func engineNames() string {
	var n []string
	for k := range engines {
		n = append(n, k)
	}
	sort.Strings(n)
	if len(n) == 0 {
		return "(none; build with -tags sherpa and/or whispercpp)"
	}
	return strings.Join(n, ", ")
}

// ModelSpec maps one logical model to its files for each engine.
type ModelSpec struct {
	ID     string
	Sherpa string // dir under <models>/sherpa/ ("" = not available)
	Kind   string // sherpa model family: whisper | transducer
	GGML   string // file under <models>/whispercpp/ ("" = not available)
}

var modelSpecs = []ModelSpec{
	{ID: "whisper-tiny.en", Sherpa: "sherpa-onnx-whisper-tiny.en", Kind: "whisper", GGML: "ggml-tiny.en.bin"},
	{ID: "whisper-base.en", Sherpa: "sherpa-onnx-whisper-base.en", Kind: "whisper", GGML: "ggml-base.en.bin"},
	{ID: "whisper-small.en", Sherpa: "sherpa-onnx-whisper-small.en", Kind: "whisper", GGML: "ggml-small.en.bin"},
	{ID: "whisper-turbo", Sherpa: "sherpa-onnx-whisper-turbo", Kind: "whisper", GGML: "ggml-large-v3-turbo.bin"},
	{ID: "whisper-turbo-q5", GGML: "ggml-large-v3-turbo-q5_0.bin"},
	{ID: "parakeet-tdt-0.6b-v2", Sherpa: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8", Kind: "transducer"},
}

func findModel(id string) (ModelSpec, error) {
	var ids []string
	for _, m := range modelSpecs {
		if m.ID == id {
			return m, nil
		}
		ids = append(ids, m.ID)
	}
	return ModelSpec{}, fmt.Errorf("unknown model %q (have %s)", id, strings.Join(ids, ", "))
}

// loadClips reads every *.wav in dir, with the reference transcript from the
// sibling .txt file.
func loadClips(dir string) ([]*Clip, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.wav"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []*Clip
	for _, p := range paths {
		s, err := readWAV(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		id := strings.TrimSuffix(filepath.Base(p), ".wav")
		ref, _ := os.ReadFile(strings.TrimSuffix(p, ".wav") + ".txt")
		out = append(out, &Clip{ID: id, Path: p, Samples: s, Ref: strings.TrimSpace(string(ref))})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no .wav clips in %s (run scripts/fetch-clips.sh)", dir)
	}
	return out, nil
}
