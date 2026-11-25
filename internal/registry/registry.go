// Package registry provides the by-name registry used by every swappable area
// (platform, speech engines, LLM providers, tools and workers).
//
// Each area declares one Registry per interface. Implementations register a
// Factory under a short name, usually from an init function, and config picks
// one by name at startup:
//
//	var STTs = registry.New[STT]("stt")
//
//	func init() { speech.STTs.Register("whisper-local", newWhisper) }
//
//	stt, err := speech.STTs.New(cfg.Speech.STT.Engine, cfg.Speech.STT.Options)
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrUnknown is returned (wrapped) by Registry.New when no factory is
// registered under the requested name.
var ErrUnknown = errors.New("registry: unknown name")

// Config is the implementation-specific part of the configuration, handed to a
// Factory. Decode fills v (a pointer to the implementation's options struct)
// from it. The config package adapts TOML tables to this interface; tests use
// Map. A Config with nothing in it leaves v unchanged, so options keep their
// defaults.
type Config interface {
	// Decode fills v, a pointer to an options struct, from the config.
	Decode(v any) error
}

// Factory builds one instance of T from its options.
type Factory[T any] func(cfg Config) (T, error)

// Registry maps names to factories for one interface type T. The zero value is
// not usable; create one with New. A Registry is safe for concurrent use.
type Registry[T any] struct {
	kind string

	mu        sync.RWMutex
	factories map[string]Factory[T]
}

// New returns an empty registry. kind names what it holds (for example "stt")
// and appears in error messages.
func New[T any](kind string) *Registry[T] {
	return &Registry[T]{kind: kind, factories: map[string]Factory[T]{}}
}

// Kind returns the name given to New.
func (r *Registry[T]) Kind() string { return r.kind }

// Register adds a factory under name. It panics if name is empty, f is nil, or
// the name is already taken, because all three are programming errors that
// should fail at startup.
func (r *Registry[T]) Register(name string, f Factory[T]) {
	if name == "" {
		panic(fmt.Sprintf("registry %s: empty name", r.kind))
	}
	if f == nil {
		panic(fmt.Sprintf("registry %s: nil factory for %q", r.kind, name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.factories[name]; dup {
		panic(fmt.Sprintf("registry %s: %q registered twice", r.kind, name))
	}
	r.factories[name] = f
}

// New builds the implementation registered under name. A nil cfg is treated as
// empty. The error wraps ErrUnknown when the name is not registered.
func (r *Registry[T]) New(name string, cfg Config) (T, error) {
	r.mu.RLock()
	f, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		var zero T
		return zero, fmt.Errorf("%s %q: %w (have %v)", r.kind, name, ErrUnknown, r.Names())
	}
	if cfg == nil {
		cfg = Map(nil)
	}
	v, err := f(cfg)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s %q: %w", r.kind, name, err)
	}
	return v, nil
}

// Has reports whether name is registered.
func (r *Registry[T]) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.factories[name]
	return ok
}

// Names returns the registered names in sorted order.
func (r *Registry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for n := range r.factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Map is a Config backed by a generic map, as produced by decoding a TOML or
// JSON table. Decode round-trips it through encoding/json, so options structs
// use json tags. Unknown keys are an error, which catches typos in config.
type Map map[string]any

// Decode implements Config.
func (m Map) Decode(v any) error {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(map[string]any(m))
	if err != nil {
		return fmt.Errorf("registry: encode options: %w", err)
	}
	return decodeStrict(b, v)
}

// JSON is a Config holding a raw JSON object.
type JSON []byte

// Decode implements Config.
func (j JSON) Decode(v any) error {
	if len(j) == 0 {
		return nil
	}
	return decodeStrict(j, v)
}

func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("registry: decode options: %w", err)
	}
	return nil
}
