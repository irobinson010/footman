//go:build native && cgo

package whispercpp

/*
#cgo LDFLAGS: -lwhisper -lggml -lggml-base -ldl
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <ggml-backend.h>
#include <whisper.h>

static int footman_verbose = 0;
static int footman_level = GGML_LOG_LEVEL_INFO;

static void footman_log(enum ggml_log_level level, const char * text, void * user) {
	(void)user;
	if (level != GGML_LOG_LEVEL_CONT) footman_level = level;
	if (footman_verbose || footman_level >= GGML_LOG_LEVEL_WARN) fputs(text, stderr);
}

static void footman_set_log(int verbose) {
	footman_verbose = verbose;
	ggml_log_set(footman_log, NULL);
	whisper_log_set(footman_log, NULL);
}

// footman_lib_path returns the file libwhisper was loaded from.
static const char * footman_lib_path(void) {
	Dl_info info;
	if (dladdr((void *)whisper_init_from_file_with_params, &info) && info.dli_fname) return info.dli_fname;
	return NULL;
}

static bool footman_abort(void * flag) { return __atomic_load_n((int *)flag, __ATOMIC_RELAXED) != 0; }

static void footman_set_abort(struct whisper_full_params * p, int * flag) {
	p->abort_callback = footman_abort;
	p->abort_callback_user_data = flag;
}

static void footman_store(int * flag, int v) { __atomic_store_n(flag, v, __ATOMIC_RELAXED); }
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/models"
)

func init() {
	speech.STTs.Register(Name, func(cfg registry.Config) (speech.STT, error) { return New(cfg) })
	accel.RegisterEngine(Name, Accelerators)
}

// libDir is the folder libwhisper was loaded from.
func libDir() string {
	p := C.footman_lib_path()
	if p == nil {
		return ""
	}
	f, err := filepath.EvalSymlinks(C.GoString(p))
	if err != nil {
		f = C.GoString(p)
	}
	return filepath.Dir(f)
}

// Accelerators lists the backends installed next to libwhisper. It only
// looks at files; nothing is loaded.
func Accelerators() []string { return installedBackends(libDir()) }

var (
	loadMu sync.Mutex
	loaded = map[string]bool{}
)

// loadBackend loads backends/<name> from dir once per process.
func loadBackend(dir, name string) {
	loadMu.Lock()
	defer loadMu.Unlock()
	if loaded[name] {
		return
	}
	p := C.CString(filepath.Join(dir, "backends", name))
	defer C.free(unsafe.Pointer(p))
	C.ggml_backend_load_all_from_path(p)
	loaded[name] = true
}

type gpuDevice struct {
	index    int // among GPU and integrated-GPU devices, as whisper.cpp counts them
	backend  string
	name     string
	discrete bool
}

func gpuDevices() []gpuDevice {
	var out []gpuDevice
	for i := C.size_t(0); i < C.ggml_backend_dev_count(); i++ {
		d := C.ggml_backend_dev_get(i)
		t := C.ggml_backend_dev_type(d)
		if t != C.GGML_BACKEND_DEVICE_TYPE_GPU && t != C.GGML_BACKEND_DEVICE_TYPE_IGPU {
			continue
		}
		out = append(out, gpuDevice{
			index:    len(out),
			backend:  C.GoString(C.ggml_backend_reg_name(C.ggml_backend_dev_backend_reg(d))),
			name:     C.GoString(C.ggml_backend_dev_description(d)),
			discrete: t == C.GGML_BACKEND_DEVICE_TYPE_GPU,
		})
	}
	return out
}

// Engine is a loaded whisper.cpp model. It is safe for concurrent use;
// transcriptions run one at a time.
type Engine struct {
	mu       sync.Mutex
	ctx      *C.struct_whisper_context
	abort    *C.int
	lang     *C.char
	threads  int
	accel    string
	device   string
	model    string
	warmup   time.Duration
	shaderAt string
}

// New loads the model named in cfg (see Options).
func New(cfg registry.Config) (*Engine, error) {
	var o Options
	if err := cfg.Decode(&o); err != nil {
		return nil, err
	}
	return Open(o)
}

// Open loads a model.
func Open(o Options) (*Engine, error) {
	o = o.withDefaults()
	path, _, err := models.Resolve(o.Model, o.ModelsDir, models.WhisperCpp)
	if err != nil {
		return nil, err
	}
	dir := o.BackendDir
	if dir == "" {
		dir = libDir()
	}
	acc, err := resolveAccelerator(o.Accelerator, installedBackends(dir))
	if err != nil {
		return nil, err
	}

	C.footman_set_log(boolInt(o.Verbose))
	e := &Engine{accel: acc, threads: o.threads(acc), model: filepath.Base(path)}
	if acc == Vulkan {
		if e.shaderAt, err = setShaderCache(o.CacheDir, os.Getenv, os.Setenv); err != nil {
			return nil, fmt.Errorf("whisper.cpp: shader cache: %w", err)
		}
	}
	loadBackend(dir, CPU)
	cp := C.whisper_context_default_params()
	cp.use_gpu = false
	if acc != CPU {
		loadBackend(dir, acc)
		dev, err := pickDevice(acc, o.Device)
		if err != nil {
			return nil, err
		}
		cp.use_gpu = true
		cp.gpu_device = C.int(dev.index)
		e.device = dev.name
		if o.FlashAttention != nil {
			cp.flash_attn = C.bool(*o.FlashAttention)
		}
	} else {
		e.device = "CPU"
	}

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	e.ctx = C.whisper_init_from_file_with_params(cpath, cp)
	if e.ctx == nil {
		return nil, fmt.Errorf("whisper.cpp could not load %s on %s (see the log above)", path, acc)
	}
	lang := o.Language
	if C.whisper_is_multilingual(e.ctx) == 0 {
		lang = "en"
	}
	e.lang = C.CString(lang)
	if lang != "auto" && C.whisper_lang_id(e.lang) < 0 {
		_ = e.Close()
		return nil, fmt.Errorf("whisper.cpp: unknown language %q", lang)
	}
	e.abort = (*C.int)(C.calloc(1, C.size_t(unsafe.Sizeof(C.int(0)))))

	if o.warmup() {
		start := time.Now()
		if _, err := e.Transcribe(context.Background(), warmupClip()); err != nil {
			_ = e.Close()
			return nil, fmt.Errorf("whisper.cpp warm-up: %w", err)
		}
		e.warmup = time.Since(start)
	}
	return e, nil
}

func pickDevice(acc string, want *int) (gpuDevice, error) {
	var devs []gpuDevice
	for _, d := range gpuDevices() {
		if strings.EqualFold(d.backend, acc) {
			devs = append(devs, d)
		}
	}
	if len(devs) == 0 {
		return gpuDevice{}, fmt.Errorf("whisper.cpp: no %s device found (is the GPU driver installed?)", acc)
	}
	if want != nil && *want >= 0 {
		for _, d := range devs {
			if d.index == *want {
				return d, nil
			}
		}
		return gpuDevice{}, fmt.Errorf("whisper.cpp: no %s device %d (have %d)", acc, *want, len(devs))
	}
	for _, d := range devs {
		if d.discrete {
			return d, nil
		}
	}
	return devs[0], nil
}

// warmupClip is a second of quiet noise: enough to run every kernel the
// encoder and decoder use, so the GPU compiles its shaders now.
func warmupClip() speech.Audio {
	s := make([]float32, audio.Speech.SampleRate)
	x := uint32(1)
	for i := range s {
		x = x*1664525 + 1013904223
		s[i] = (float32(x>>8)/float32(1<<24) - 0.5) * 0.01
	}
	return speech.Audio{Format: audio.Speech, Samples: s}
}

// Transcribe implements speech.STT. Audio in another format is converted to
// 16 kHz mono first. Cancelling ctx aborts the transcription.
func (e *Engine) Transcribe(ctx context.Context, a speech.Audio) (speech.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return speech.Transcript{}, err
	}
	a = audio.ToSpeech(a)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx == nil {
		return speech.Transcript{}, errors.New("whisper.cpp: engine closed")
	}
	if len(a.Samples) == 0 {
		return speech.Transcript{Language: C.GoString(e.lang)}, nil
	}
	C.footman_store(e.abort, 0)
	stop := context.AfterFunc(ctx, func() { C.footman_store(e.abort, 1) })
	defer stop()

	p := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	p.n_threads = C.int(e.threads)
	p.language = e.lang
	p.translate = false
	p.no_context = true
	p.print_progress = false
	p.print_realtime = false
	p.print_timestamps = false
	p.print_special = false
	C.footman_set_abort(&p, e.abort)
	rc := C.whisper_full(e.ctx, p, (*C.float)(unsafe.Pointer(&a.Samples[0])), C.int(len(a.Samples)))
	if err := ctx.Err(); err != nil {
		return speech.Transcript{}, err
	}
	if rc != 0 {
		return speech.Transcript{}, fmt.Errorf("whisper.cpp: transcription failed (code %d)", int(rc))
	}

	var (
		parts []string
		psum  float64
		pn    int
	)
	eot := C.whisper_token_eot(e.ctx)
	for i := C.int(0); i < C.whisper_full_n_segments(e.ctx); i++ {
		if t := strings.TrimSpace(C.GoString(C.whisper_full_get_segment_text(e.ctx, i))); t != "" {
			parts = append(parts, t)
		}
		for j := C.int(0); j < C.whisper_full_n_tokens(e.ctx, i); j++ {
			if C.whisper_full_get_token_id(e.ctx, i, j) >= eot {
				continue // special tokens
			}
			psum += float64(C.whisper_full_get_token_p(e.ctx, i, j))
			pn++
		}
	}
	t := speech.Transcript{Text: strings.Join(parts, " "), Language: C.GoString(C.whisper_lang_str(C.whisper_full_lang_id(e.ctx)))}
	if pn > 0 {
		t.Confidence = float32(math.Max(0, math.Min(1, psum/float64(pn))))
	}
	return t, nil
}

// Info describes the loaded engine, for logs and status.
func (e *Engine) Info() string {
	s := fmt.Sprintf("whisper.cpp %s, %s on %s, %d threads", C.GoString(C.whisper_version()), e.model, e.device, e.threads)
	if e.warmup > 0 {
		s += fmt.Sprintf(", warm-up %s", e.warmup.Round(time.Millisecond))
	}
	if e.shaderAt != "" {
		s += ", shader cache " + e.shaderAt
	}
	return s
}

// Accelerator is the backend in use: cpu, vulkan or cuda.
func (e *Engine) Accelerator() string { return e.accel }

// Warmup is how long the warm-up transcription took (0 if none ran).
func (e *Engine) Warmup() time.Duration { return e.warmup }

// ShaderCache is the folder the Vulkan shader cache was pointed at, if any.
func (e *Engine) ShaderCache() string { return e.shaderAt }

// Close frees the model. It is safe to call more than once.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx != nil {
		C.whisper_free(e.ctx)
		e.ctx = nil
	}
	if e.lang != nil {
		C.free(unsafe.Pointer(e.lang))
		e.lang = nil
	}
	if e.abort != nil {
		C.free(unsafe.Pointer(e.abort))
		e.abort = nil
	}
	return nil
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
