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
// unloaded. A nil one means the area has no engine.
type reloaders struct {
	stt func() (speech.STT, error)
	vad func() (speech.VAD, error)
	tts func() (speech.TTS, error)
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
	closes []func() error
	after  func() time.Duration
}

// manageSpeech replaces the engines in d with ones that unload after `after`
// of idle time and load again on use, and returns what reports and closes
// them. Areas without an engine are left alone.
func manageSpeech(d *core.Deps, impl map[string]string, rl reloaders, after func() time.Duration) *speechModels {
	m := &speechModels{after: after}
	// Unloading only lowers the daemon's memory if the freed heap goes back
	// to the OS: the engines' (C) and the recordings' (Go).
	o := idle.Options{After: after, OnUnload: func() {
		memtrim.Trim()
		debug.FreeOSMemory()
	}}
	add := func(h interface {
		State() idle.State
		Close() error
	}) {
		m.states = append(m.states, h.State)
		m.closes = append(m.closes, h.Close)
	}
	if d.STT != nil && rl.stt != nil {
		h := idle.New("stt", impl["stt"], d.STT, rl.stt, o)
		d.STT = idle.STT{H: h}
		add(h)
	}
	if d.VAD != nil && rl.vad != nil {
		h := idle.New("vad", impl["vad"], d.VAD, rl.vad, o)
		d.VAD = idle.VAD{H: h}
		add(h)
	}
	if d.TTS != nil && rl.tts != nil {
		h := idle.New("tts", impl["tts"], d.TTS, rl.tts, o)
		d.TTS = idle.TTS{H: h}
		add(h)
	}
	return m
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
	for _, f := range m.states {
		s := f()
		st.Models = append(st.Models, api.ModelState{
			Name: s.Name, Engine: s.Engine, Loaded: s.Loaded, Since: s.Since, Loads: s.Loads, InUse: s.InUse,
		})
	}
	return st
}
