//go:build sherpa

package main

import (
	"fmt"
	"path/filepath"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/v2/sherpa_onnx"
)

// The official Go bindings ship prebuilt shared libraries (CPU only) inside the
// per-OS module, so `go build -tags sherpa` needs a C compiler but no
// cmake/CUDA/Vulkan. GPU support means swapping in the -gpu release libraries
// at run time (LD_LIBRARY_PATH) and passing -accel cuda.

func init() {
	engines["sherpa"] = engineEntry{
		New:    func() Engine { return &sherpaEngine{} },
		Accels: []string{"cpu", "cuda", "directml"}, // cuda and directml need the matching native libraries
		Cgo:    true,
	}
}

type sherpaEngine struct {
	rec   *sherpa.OfflineRecognizer
	info  string
	bytes int64
}

func (e *sherpaEngine) ModelBytes() int64 { return e.bytes }

func (e *sherpaEngine) Info() string { return e.info }

func (e *sherpaEngine) Load(o LoadOpts) error {
	if o.Model.Sherpa == "" {
		return fmt.Errorf("model %s has no sherpa-onnx build", o.Model.ID)
	}
	dir := filepath.Join(o.ModelsDir, "sherpa", o.Model.Sherpa)
	variant := o.Variant
	if variant == "" || variant == "auto" {
		variant = "int8"
		if o.Accel != "cpu" {
			variant = "fp32"
		}
	}
	cfg := sherpa.OfflineRecognizerConfig{
		FeatConfig:     sherpa.FeatureConfig{SampleRate: 16000, FeatureDim: 80},
		DecodingMethod: "greedy_search",
	}
	cfg.ModelConfig.NumThreads = o.Threads
	cfg.ModelConfig.Provider = o.Accel
	switch o.Model.Kind {
	case "whisper":
		enc, err := pickOnnx(dir, "*encoder", variant)
		if err != nil {
			return err
		}
		dec, err := pickOnnx(dir, "*decoder", variant)
		if err != nil {
			return err
		}
		tok, err := first(dir, "*tokens.txt")
		if err != nil {
			return err
		}
		cfg.ModelConfig.Whisper = sherpa.OfflineWhisperModelConfig{
			Encoder: enc, Decoder: dec, Language: "en", Task: "transcribe", TailPaddings: -1,
		}
		cfg.ModelConfig.Tokens = tok
		e.bytes = fileBytes(enc, dec)
	case "transducer":
		enc, err := pickOnnx(dir, "encoder*", variant)
		if err != nil {
			return err
		}
		dec, err := pickOnnx(dir, "decoder*", variant)
		if err != nil {
			return err
		}
		join, err := pickOnnx(dir, "joiner*", variant)
		if err != nil {
			return err
		}
		tok, err := first(dir, "tokens.txt")
		if err != nil {
			return err
		}
		cfg.ModelConfig.Transducer = sherpa.OfflineTransducerModelConfig{Encoder: enc, Decoder: dec, Joiner: join}
		cfg.ModelConfig.Tokens = tok
		cfg.ModelConfig.ModelType = "nemo_transducer"
		e.bytes = fileBytes(enc, dec, join)
	default:
		return fmt.Errorf("unknown sherpa model kind %q", o.Model.Kind)
	}
	e.rec = sherpa.NewOfflineRecognizer(&cfg)
	if e.rec == nil {
		return fmt.Errorf("sherpa-onnx could not create the recognizer (provider %q; see its stderr output)", o.Accel)
	}
	e.info = fmt.Sprintf("sherpa-onnx %s, onnxruntime %s, variant %s, provider %s",
		sherpa.GetVersion(), sherpa.GetOnnxruntimeVersion(), variant, o.Accel)
	return nil
}

func (e *sherpaEngine) Transcribe(c *Clip) (Result, error) {
	stream := sherpa.NewOfflineStream(e.rec)
	defer sherpa.DeleteOfflineStream(stream)
	stream.AcceptWaveform(16000, c.Samples)
	t := time.Now()
	e.rec.Decode(stream)
	inner := time.Since(t)
	r := stream.GetResult()
	return Result{Text: r.Text, Inner: inner}, nil
}

func (e *sherpaEngine) Close() {
	if e.rec != nil {
		sherpa.DeleteOfflineRecognizer(e.rec)
		e.rec = nil
	}
}

// pickOnnx finds <glob>*.onnx, preferring the int8 or fp32 file as asked and
// falling back to whichever exists.
func pickOnnx(dir, glob, variant string) (string, error) {
	int8s, _ := filepath.Glob(filepath.Join(dir, glob+".int8.onnx"))
	all, _ := filepath.Glob(filepath.Join(dir, glob+".onnx"))
	switch {
	case variant == "int8" && len(int8s) > 0:
		return int8s[0], nil
	case variant == "fp32" && len(all) > 0:
		return all[0], nil
	case len(all) > 0:
		return all[0], nil
	case len(int8s) > 0:
		return int8s[0], nil
	}
	return "", fmt.Errorf("no %s*.onnx in %s (run scripts/fetch-models.sh)", glob, dir)
}

func first(dir, glob string) (string, error) {
	m, _ := filepath.Glob(filepath.Join(dir, glob))
	if len(m) == 0 {
		return "", fmt.Errorf("no %s in %s (run scripts/fetch-models.sh)", glob, dir)
	}
	return m[0], nil
}
