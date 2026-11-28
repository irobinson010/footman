package main

import (
	"errors"
	"runtime/debug"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/idle"
	"github.com/irobinson010/footman/internal/speech/memtrim"
)

// reloaders build a fresh engine for an area, to load it again after it was
// unloaded. A nil one means the area has no engine. Text-to-speech is
// managed by ttsEngines, which keeps an idle Holder per engine.
type reloaders struct {
	stt func() (speech.STT, error)
	// sttEngine, if set, is the registry name of the engine stt built last
	// (a reload can fall back to another engine).
	sttEngine func() string
	vad       func() (speech.VAD, error)
	// tts builds a text-to-speech engine for newTTSEngines: the one
	// registered as engine (or its stand-in), loading v's model first, and
	// the registry name it built ("" for none).
	tts func(engine string, v speech.Voice) (speech.TTS, string, error)
}

// reloader returns a function that builds the engine registered as name again
// with the options it was first built with. The fake never takes options.
func reloader[T any](reg *registry.Registry[T], name string, opts registry.Config) func() (T, error) {
	if name == "" {
		return nil
	}
	if name == fakeName {
		opts = nil
	}
	return func() (T, error) { return reg.New(name, opts) }
}

// unloadAfter is speech.unload_after; 0 (or an unreadable value, which
// config validation already rejects) means never.
func unloadAfter(cfg *config.Config) time.Duration {
	d, err := time.ParseDuration(cfg.String("speech.unload_after"))
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// speechModels are the daemon's speech engines that unload when idle.
type speechModels struct {
	states []func() idle.State
	more   []func() []idle.State // areas with several engines (TTS)
	closes []func() error
	after  func() time.Duration
}

// manageSpeech replaces the engines in d with ones that unload after `after`
// of idle time and load again on use, and returns what reports and closes
// them. Areas without an engine are left alone.
func manageSpeech(d *core.Deps, impl map[string]string, rl reloaders, after func() time.Duration) *speechModels {
	m := &speechModels{after: after}
	o := idleOptions(after)
	add := func(h interface {
		State() idle.State
		Close() error
	}) {
		m.states = append(m.states, h.State)
		m.closes = append(m.closes, h.Close)
	}
	if d.STT != nil && rl.stt != nil {
		so := o
		so.Engine = rl.sttEngine
		h := idle.New("stt", impl["stt"], d.STT, rl.stt, so)
		d.STT = idle.STT{H: h}
		add(h)
	}
	if d.VAD != nil && rl.vad != nil {
		h := idle.New("vad", impl["vad"], d.VAD, rl.vad, o)
		d.VAD = idle.VAD{H: h}
		add(h)
	}
	return m
}

// idleOptions are the idle-unloading options of every speech engine.
func idleOptions(after func() time.Duration) idle.Options {
	// Unloading only lowers the daemon's memory if the freed heap goes back
	// to the OS: the engines' (C) and the recordings' (Go).
	return idle.Options{After: after, OnUnload: func() {
		memtrim.Trim()
		debug.FreeOSMemory()
	}}
}

// addTTS reports the text-to-speech engines in status and closes them with
// the rest.
func (m *speechModels) addTTS(t *ttsEngines) {
	m.more = append(m.more, t.States)
	m.closes = append(m.closes, t.Close)
}

// Close closes every engine, loaded or not.
func (m *speechModels) Close() {
	var err error
	for _, c := range m.closes {
		err = errors.Join(err, c())
	}
}

// Status is the models part of status.get.
func (m *speechModels) Status() api.ModelsStatus {
	st := api.ModelsStatus{UnloadAfter: m.after().String()}
	var all []idle.State
	for _, f := range m.states {
		all = append(all, f())
	}
	for _, f := range m.more {
		all = append(all, f()...)
	}
	for _, s := range all {
		st.Models = append(st.Models, api.ModelState{
			Name: s.Name, Engine: s.Engine, Loaded: s.Loaded, Since: s.Since, Loads: s.Loads, InUse: s.InUse,
		})
	}
	return st
}
