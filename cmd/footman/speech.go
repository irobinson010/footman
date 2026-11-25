package main

import (
	"io"
	"path/filepath"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/registry"
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
	engine := cfg.String("speech.stt.engine")
	avail := accel.Engines()
	if _, native := avail[engine]; engine != "" && engine != accel.Auto && !native {
		return sttPlan{name: engine}, nil
	}
	c, err := accel.Choose(hw(), accel.Request{
		Engine:      engine,
		Model:       cfg.String("speech.stt.model"),
		Accelerator: cfg.String("speech.stt.accelerator"),
		Language:    cfg.String("user.language"),
	}, avail)
	if err != nil {
		return sttPlan{}, err
	}
	return sttPlan{name: c.Engine, opts: sttOptions(c, cfg.String("user.language"), dirs), choice: &c}, nil
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

// engineInfo is what the native engines report about themselves.
type engineInfo interface{ Info() string }

// shaderCached is implemented by engines that keep a GPU shader cache.
type shaderCached interface {
	ShaderCache() string
	Warmup() time.Duration
}

// logSpeech logs what the speech engines loaded, and records the shader
// cache in the install receipt so uninstall removes it (ADR-0023).
func logSpeech(logf func(string, ...any), dataDir string, stt, vad any) {
	for _, e := range []struct {
		area string
		v    any
	}{{"speech-to-text", stt}, {"VAD", vad}} {
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
