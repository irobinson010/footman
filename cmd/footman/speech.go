package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/sherpa"
)

// speechDirs are where the speech engines find their models and keep their
// caches.
type speechDirs struct {
	// models is the models folder (<data>/models).
	models string
	// cache is Footman's cache folder; the Vulkan shader cache goes in it.
	cache string
}

func modelsDir(data string) string { return filepath.Join(data, "models") }

// defaultVAD is the VAD the daemon uses.
const defaultVAD = sherpa.SileroName

// sttPlan is the speech-to-text engine the daemon will build.
type sttPlan struct {
	// name is the registry name.
	name string
	// opts are the factory's options (nil for engines outside the accel
	// table, such as the fake).
	opts registry.Config
	// choice is what accel.Choose picked, when it ran.
	choice *accel.Choice
}

// planSTT turns speech.stt.engine, .model and .accelerator (and
// user.language) into an engine and its options. For "auto" (the default) or
// an engine that registered with accel, it applies the ADR-0024 table to the
// detected hardware and the engines in this build; any other name is used
// as is.
func planSTT(cfg *config.Config, dirs speechDirs, hw func() accel.Hardware) (sttPlan, error) {
	return planSTTWith(cfg, dirs, hw, accel.Engines(), nil)
}

// planSTTWith is planSTT for the engines avail, with adjust (if not nil)
// applied to the request read from the config.
func planSTTWith(cfg *config.Config, dirs speechDirs, hw func() accel.Hardware, avail map[string][]string, adjust func(*accel.Request)) (sttPlan, error) {
	engine := cfg.String("speech.stt.engine")
	if _, native := avail[engine]; engine != "" && engine != accel.Auto && !native {
		return sttPlan{name: engine}, nil
	}
	req := accel.Request{
		Engine:      engine,
		Model:       cfg.String("speech.stt.model"),
		Accelerator: cfg.String("speech.stt.accelerator"),
		Language:    cfg.String("user.language"),
	}
	if adjust != nil {
		adjust(&req)
	}
	c, err := accel.Choose(hw(), req, avail)
	if err != nil {
		return sttPlan{}, err
	}
	return sttPlan{name: c.Engine, opts: sttOptions(c, cfg.String("user.language"), dirs), choice: &c}, nil
}

// onGPU reports whether the plan runs on a GPU.
func (p sttPlan) onGPU() bool {
	return p.choice != nil && p.choice.Accelerator != accel.CPU
}

// sttOpened is a speech-to-text engine the daemon built.
type sttOpened struct {
	stt speech.STT
	// impl is the registry name it was built under ("" for none).
	impl string
	// plan is what it was built from; after a fallback, the CPU plan.
	plan sttPlan
	// fallback is non-empty when speech.stt.accelerator is "auto" and the
	// GPU engine failed to open, so the CPU one was built instead. It says
	// why.
	fallback string
}

// openSTT builds the speech-to-text engine for plan with open. When the plan
// is a GPU one and speech.stt.accelerator is "auto" (or unset), a failure to
// open it (a full GPU, a broken driver) is not fatal: it logs one warning
// and builds what accel.Choose picks for accelerator "cpu". With an explicit
// accelerator (gpu, vulkan, cuda) the failure is returned, with a hint to try
// "cpu". hw and avail are the hardware and engines the plan was made from.
func openSTT(cfg *config.Config, dirs speechDirs, hw func() accel.Hardware, avail map[string][]string, plan sttPlan, open func(sttPlan) (speech.STT, string, error), logf func(string, ...any)) (sttOpened, error) {
	stt, impl, err := open(plan)
	if err == nil || !plan.onGPU() {
		return sttOpened{stt: stt, impl: impl, plan: plan}, err
	}
	if a := cfg.String("speech.stt.accelerator"); a != "" && a != accel.Auto {
		return sttOpened{}, fmt.Errorf("%w\n(speech.stt.accelerator is %q, so there is no fallback; set it to \"cpu\" to run speech-to-text on the CPU, or to \"auto\" to fall back to it)", err, a)
	}
	cpu := func(r *accel.Request) { r.Accelerator = accel.CPU }
	fb, ferr := planSTTWith(cfg, dirs, hw, avail, cpu)
	if ferr == nil && fb.choice.Engine != plan.choice.Engine {
		// A model chosen for the GPU engine does not suit another engine.
		fb, ferr = planSTTWith(cfg, dirs, hw, avail, func(r *accel.Request) { cpu(r); r.Model = accel.Auto })
	}
	if ferr != nil {
		return sttOpened{}, fmt.Errorf("%w\n(and no CPU fallback: %w)", err, ferr)
	}
	reason := firstLine(err.Error())
	logf("WARNING: speech-to-text on %s failed (%s); falling back to the CPU: %s. Set speech.stt.accelerator = \"cpu\" to skip the GPU attempt", plan.choice.Accelerator, reason, describePlan(fb))
	stt, impl, ferr = open(fb)
	if ferr != nil {
		return sttOpened{}, fmt.Errorf("%w\n(the CPU fallback failed too: %w)", err, ferr)
	}
	return sttOpened{stt: stt, impl: impl, plan: fb, fallback: fmt.Sprintf("%s on %s failed (%s)", plan.choice.Engine, plan.choice.Accelerator, reason)}, nil
}

// sttSource builds the speech-to-text engine at start and again after each
// idle unload (ADR-0028). It always starts from what accel.Choose picked
// first, so after a fallback to the CPU the next load tries the GPU again
// (the GPU may have been freed), and falls back again if it is still full.
// The warning is logged when the engine falls back, not on every retry.
type sttSource struct {
	cfg   *config.Config
	dirs  speechDirs
	hw    func() accel.Hardware
	avail map[string][]string
	open  func(sttPlan) (speech.STT, string, error)
	logf  func(string, ...any)
	// first is the plan from planSTT.
	first sttPlan

	mu  sync.Mutex
	cur sttOpened // the engine built last
}

// Open builds the engine for the first time.
func (s *sttSource) Open() (sttOpened, error) {
	o, err := openSTT(s.cfg, s.dirs, s.hw, s.avail, s.first, s.open, s.logf)
	if err == nil {
		s.set(o)
	}
	return o, err
}

// Reload builds the engine again after it was unloaded.
func (s *sttSource) Reload() (speech.STT, error) {
	was := s.current()
	logf := s.logf
	if was.fallback != "" {
		// Already on the CPU: trying the GPU again and failing is not news.
		logf = func(string, ...any) {}
	}
	o, err := openSTT(s.cfg, s.dirs, s.hw, s.avail, s.first, s.open, logf)
	if err != nil {
		return nil, err
	}
	if was.fallback != "" && o.fallback == "" {
		s.logf("speech-to-text: the GPU opened again; using %s", describePlan(o.plan))
	}
	s.set(o)
	return o.stt, nil
}

// Engine is the registry name of the engine built last.
func (s *sttSource) Engine() string { return s.current().impl }

// Path is the speech-to-text path of the engine built last, for status.
func (s *sttSource) Path() string { return s.current().path() }

func (s *sttSource) current() sttOpened {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *sttSource) set(o sttOpened) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = o
}

// firstLine is s up to its first line break.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// path is the one-line description of the speech-to-text path for status:
// what is in use, and why it is not the first choice after a fallback.
func (o sttOpened) path() string {
	if o.plan.choice == nil {
		return ""
	}
	s := o.plan.choice.String()
	if o.fallback != "" {
		s += "; fell back from " + o.fallback
	}
	return s
}

// sttOptions are the options a native speech-to-text engine takes for a
// choice.
func sttOptions(c accel.Choice, language string, dirs speechDirs) registry.Map {
	return registry.Map{
		"model":       c.Model,
		"accelerator": c.Accelerator,
		"language":    language,
		"models_dir":  dirs.models,
		"cache_dir":   dirs.cache,
	}
}

// vadOptions are the options for the VAD named name. minSilence is
// speech.vad.min_silence in seconds.
func vadOptions(name string, dirs speechDirs, minSilence float64) registry.Config {
	if name == sherpa.SileroName {
		return registry.Map{"models_dir": dirs.models, "min_silence": minSilence}
	}
	return nil
}

// wakeOptions are the options for the wake-word engine named name: keywords
// (personaWakeWords), with the sensitivity from speech.wake.*.
func wakeOptions(name string, dirs speechDirs, cfg *config.Config, keywords []string) registry.Config {
	if name != sherpa.KWSName {
		return nil
	}
	return registry.Map{
		"models_dir": dirs.models,
		"keywords":   keywords,
		"boost":      cfg.Float("speech.wake.boost"),
		"threshold":  cfg.Float("speech.wake.threshold"),
	}
}

// engineInfo is what the native engines report about themselves.
type engineInfo interface{ Info() string }

// shaderCached is implemented by engines that keep a GPU shader cache.
type shaderCached interface {
	ShaderCache() string
	Warmup() time.Duration
}

// logSpeech logs what the speech engines loaded, and records the shader
// cache in the install receipt so uninstall removes it (ADR-0023).
func logSpeech(logf func(string, ...any), dataDir string, stt, vad, wake any) {
	for _, e := range []struct {
		area string
		v    any
	}{{"speech-to-text", stt}, {"VAD", vad}, {"wake word", wake}} {
		if i, ok := e.v.(engineInfo); ok {
			logf("%s: %s", e.area, i.Info())
		}
	}
	if s, ok := stt.(shaderCached); ok && s.ShaderCache() != "" {
		if err := install.RecordIn(dataDir, install.Entry{Kind: install.KindTree, Group: install.GroupCache, Path: s.ShaderCache()}); err != nil {
			logf("could not record the shader cache in the install receipt: %v", err)
		}
	}
}

func closeIfCloser(v any) {
	if c, ok := v.(io.Closer); ok {
		_ = c.Close()
	}
}

func describePlan(p sttPlan) string {
	if p.choice == nil {
		return p.name
	}
	return p.choice.String()
}
