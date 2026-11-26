// Package speechsetup is the speech part of `footman setup` (ADR-0028): it
// fetches the models for the engine the hardware calls for, warms up the GPU
// path so the shader compile never happens mid-dictation, benchmarks the GPU
// path against the CPU path through the speech.STT interface, and records a
// CPU choice in the config when the GPU is slower (a weak integrated GPU).
//
// Everything that touches the machine comes in through Env (downloads,
// building engines, the clock), so the logic is tested with fakes; the native
// engines are only involved in cmd/footman, which fills Env in.
package speechsetup

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/models"
)

// Benchmark defaults: five seconds of audio, repeated until Budget has
// passed (at least once, at most MaxRuns times).
const (
	ClipLength   = 5 * time.Second
	DefaultRuns  = 5
	DefaultSpend = 5 * time.Second
)

// VADModel is the model the default VAD (Silero) loads; setup fetches it
// with the speech-to-text model.
const VADModel = "silero-vad"

// WakeModel is the wake-word keyword-spotting model; setup fetches it when
// input.wake_word is on.
const WakeModel = "kws-zipformer-gigaspeech"

// Env is everything Run needs from the machine.
type Env struct {
	// ConfigPath is the config file. It must exist.
	ConfigPath string
	// ModelsDir is where models are downloaded.
	ModelsDir string
	// Hardware and Engines are what accel.Detect and accel.Engines return.
	Hardware accel.Hardware
	Engines  map[string][]string

	// Step reports one line of progress.
	Step func(format string, a ...any)
	// Progress reports download progress of the model id; may be nil.
	Progress func(id string, done, total int64)
	// Record puts a model (or, with isDir, the models folder) on the
	// install receipt before it is downloaded; may be nil.
	Record func(path, id string, isDir bool) error

	// Fetch downloads a model (default models.Fetch). Tests replace it.
	Fetch func(ctx context.Context, m models.Model, dir string, o models.FetchOptions) (string, error)
	// Open builds the speech-to-text engine for a choice, which loads the
	// model and, for whisper.cpp, warms it up. An engine that is an
	// io.Closer is closed after use.
	Open func(ctx context.Context, c accel.Choice) (speech.STT, error)

	// Now is the clock (default time.Now). Tests replace it.
	Now func() time.Time
	// Budget is how long each path is benchmarked (default 5 s); MaxRuns
	// caps the number of timed runs (default 5).
	Budget  time.Duration
	MaxRuns int
	// Clip is the audio to benchmark with (default Clip(ClipLength)).
	Clip speech.Audio
}

// Result says what setup did.
type Result struct {
	// Choice is the engine setup settled on.
	Choice accel.Choice
	// Skipped is set when there was nothing to do, with the reason.
	Skipped string
	// GPU and CPU are the benchmark results; zero when not run.
	GPU, CPU Timing
	// PickedCPU is true when the GPU was slower (or broken) and
	// speech.stt.accelerator = "cpu" was written to the config.
	PickedCPU bool
}

// Timing is how one path did.
type Timing struct {
	// Choice is what was measured.
	Choice accel.Choice
	// Load is how long building the engine took: the warm-up, which on a
	// GPU is the shader compile.
	Load time.Duration
	// Median is the median time to transcribe the clip; Runs the number of
	// timed runs.
	Median time.Duration
	Runs   int
	// Err is why the path could not be measured.
	Err error
}

// OK reports whether the path was measured.
func (t Timing) OK() bool { return t.Err == nil && t.Runs > 0 }

// Run does the speech part of setup. It returns an error for a problem the
// user must fix (a bad config, a failed download); a GPU that cannot run is
// not an error, it makes setup choose the CPU.
func Run(ctx context.Context, env Env) (Result, error) {
	env.defaults()
	var res Result
	if len(env.Engines) == 0 {
		res.Skipped = "this build has no local speech engines (build with -tags native); skipping models, warm-up and benchmark"
		env.Step("speech       %s", res.Skipped)
		return res, nil
	}
	cfg, err := config.Load(env.ConfigPath)
	if err != nil {
		return res, err
	}
	req := accel.Request{
		Engine:      cfg.String("speech.stt.engine"),
		Model:       cfg.String("speech.stt.model"),
		Accelerator: cfg.String("speech.stt.accelerator"),
		Language:    cfg.String("user.language"),
	}
	primary, err := accel.Choose(env.Hardware, req, env.Engines)
	if err != nil {
		return res, fmt.Errorf("speech setup: %w", err)
	}
	res.Choice = primary
	env.Step("speech       %s", primary)

	fetch := []string{primary.Model, VADModel}
	if cfg.Bool("input.wake_word") {
		fetch = append(fetch, WakeModel)
	}
	if err := env.fetch(ctx, fetch...); err != nil {
		return res, err
	}

	gpu := primary.Accelerator != accel.CPU
	if !gpu {
		env.Step("speech       CPU engine, nothing to warm up")
		return res, nil
	}
	// Compare with the CPU path unless the user fixed the accelerator.
	var cpu accel.Choice
	compare := accel.Auto == req.Accelerator || req.Accelerator == ""
	if compare {
		creq := req
		creq.Accelerator = accel.CPU
		c, err := accel.Choose(env.Hardware, creq, env.Engines)
		if err != nil {
			env.Step("speech       no CPU engine to compare with (%v); keeping %s", err, primary.Accelerator)
			compare = false
		} else {
			cpu = c
			if err := env.fetch(ctx, cpu.Model); err != nil {
				return res, err
			}
		}
	}

	env.Step("speech       warming up %s: the first run compiles GPU shaders and can take 30 s; it is kept for later", primary.Accelerator)
	res.GPU = env.measure(ctx, primary)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	report(env.Step, "GPU", res.GPU)
	if !compare {
		if !res.GPU.OK() {
			env.Step("speech       warning: the %s path did not work (%v); set speech.stt.accelerator = \"cpu\" if dictation fails", primary.Accelerator, res.GPU.Err)
		}
		return res, nil
	}

	env.Step("speech       benchmarking the CPU path: %s", cpu)
	res.CPU = env.measure(ctx, cpu)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	report(env.Step, "CPU", res.CPU)

	switch why := cpuWins(res.GPU, res.CPU); {
	case why != "":
		res.PickedCPU, res.Choice = true, cpu
		comment := "Set by `footman setup` on " + env.Now().Format("2006-01-02") + ": " + why + ".\n" +
			"Delete this line to let footman choose the accelerator again."
		if err := config.SetFileComment(env.ConfigPath, "speech.stt.accelerator", accel.CPU, comment); err != nil {
			return res, fmt.Errorf("save the CPU choice: %w", err)
		}
		env.Step("speech       using the CPU: %s (saved as speech.stt.accelerator in %s)", why, env.ConfigPath)
	case !res.GPU.OK():
		env.Step("speech       neither path could be measured; keeping %s", primary.Accelerator)
	default:
		env.Step("speech       using %s: it is faster than the CPU", primary.Accelerator)
	}
	return res, nil
}

// cpuWins says why the CPU should be used, or "" when the GPU should stay.
func cpuWins(gpu, cpu Timing) string {
	switch {
	case !cpu.OK():
		return ""
	case !gpu.OK():
		return fmt.Sprintf("the %s path did not work (%v)", gpu.Choice.Accelerator, gpu.Err)
	case gpu.Median > cpu.Median:
		return fmt.Sprintf("the %s path took %s for %.0f s of audio, slower than the CPU path's %s",
			gpu.Choice.Accelerator, round(gpu.Median), ClipLength.Seconds(), round(cpu.Median))
	}
	return ""
}

func round(d time.Duration) time.Duration {
	if d >= time.Second {
		return d.Round(10 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}

func report(step func(string, ...any), name string, t Timing) {
	if !t.OK() {
		step("speech       %s: failed: %v", name, t.Err)
		return
	}
	step("speech       %s: loaded and warmed up in %s; %.0f s of audio takes %s (median of %d)",
		name, round(t.Load), ClipLength.Seconds(), round(t.Median), t.Runs)
}

func (e *Env) defaults() {
	if e.Step == nil {
		e.Step = func(string, ...any) {}
	}
	if e.Fetch == nil {
		e.Fetch = models.Fetch
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Budget <= 0 {
		e.Budget = DefaultSpend
	}
	if e.MaxRuns <= 0 {
		e.MaxRuns = DefaultRuns
	}
	if len(e.Clip.Samples) == 0 {
		e.Clip = Clip(ClipLength)
	}
}

// fetch downloads the named models that are not already there. Models that
// are present are not touched, so setup works offline.
func (e *Env) fetch(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		m, ok := models.Lookup(id)
		if !ok {
			return fmt.Errorf("speech setup: unknown model %q", id)
		}
		if m.Installed(e.ModelsDir) {
			if e.Record != nil {
				if err := e.Record(e.ModelsDir, "", true); err != nil {
					return err
				}
				if err := e.Record(m.Path(e.ModelsDir), id, false); err != nil {
					return err
				}
			}
			e.Step("model        %s already downloaded", id)
			continue
		}
		e.Step("model        downloading %s (%d MB)", id, m.Size>>20)
		o := models.FetchOptions{Progress: func(done, total int64) {
			if e.Progress != nil {
				e.Progress(id, done, total)
			}
		}}
		if e.Record != nil {
			o.Record = func(path string, isDir bool) error { return e.Record(path, id, isDir) }
		}
		start := e.Now()
		p, err := e.Fetch(ctx, m, e.ModelsDir, o)
		if err != nil {
			return fmt.Errorf("download %s: %w\n(setup works offline once the models are in %s; run `footman models fetch` when online, or pass --skip-models)", id, err, e.ModelsDir)
		}
		e.Step("model        %s ready at %s (%s)", id, p, e.Now().Sub(start).Round(time.Second))
	}
	return nil
}

// measure builds the engine for c and times it on the clip. The first
// transcription after loading is not timed.
func (e *Env) measure(ctx context.Context, c accel.Choice) (t Timing) {
	t.Choice = c
	start := e.Now()
	stt, err := e.Open(ctx, c)
	if err != nil {
		t.Err = err
		return t
	}
	defer func() {
		if cl, ok := stt.(io.Closer); ok {
			_ = cl.Close()
		}
	}()
	if _, err := stt.Transcribe(ctx, e.Clip); err != nil {
		t.Err = err
		return t
	}
	t.Load = e.Now().Sub(start) // the load and the first, untimed run
	var runs []time.Duration
	begin := e.Now()
	for len(runs) < e.MaxRuns && (len(runs) == 0 || e.Now().Sub(begin) < e.Budget) {
		if ctx.Err() != nil {
			t.Err = ctx.Err()
			return t
		}
		s := e.Now()
		if _, err := stt.Transcribe(ctx, e.Clip); err != nil {
			t.Err = err
			return t
		}
		runs = append(runs, e.Now().Sub(s))
	}
	slices.Sort(runs)
	t.Median, t.Runs = runs[len(runs)/2], len(runs)
	return t
}

// Clip returns d of deterministic speech-like audio for benchmarking: a
// buzzing voice at about 120 Hz with a syllable rhythm. Engines see it as
// speech-length audio; what matters is how long they take, which for these
// engines depends on the length of the audio rather than on what is said.
func Clip(d time.Duration) speech.Audio {
	rate := audio.Speech.SampleRate
	n := int(d.Seconds() * float64(rate))
	s := make([]float32, n)
	for i := range s {
		t := float64(i) / float64(rate)
		env := 0.5 + 0.5*math.Sin(2*math.Pi*3.5*t) // about 3.5 syllables a second
		var v float64
		for h := 1; h <= 8; h++ {
			v += math.Sin(2*math.Pi*120*float64(h)*t) / float64(h)
		}
		s[i] = float32(0.2 * env * v / 2)
	}
	return speech.Audio{Format: audio.Speech, Samples: s}
}
