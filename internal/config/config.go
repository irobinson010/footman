package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// CurrentVersion is the config format version this build writes and expects.
const CurrentVersion = 1

// SecretRef points at a secret in the OS keychain ("keychain:<name>"). It is
// never resolved here.
type SecretRef struct{ Name string }

// String returns the reference in file form.
func (s SecretRef) String() string { return "keychain:" + s.Name }

// Config is a validated configuration. Values are stored by dotted path with
// the Go type given by the schema: string, bool, int64, float64, []string or
// SecretRef. Every schema key without a wildcard always has a value.
type Config struct {
	values  map[string]any
	plugins map[string]map[string]any
}

// Defaults returns a Config holding every default.
func Defaults() *Config {
	c := &Config{values: map[string]any{}, plugins: map[string]map[string]any{}}
	for _, k := range schema {
		if k.Default != nil {
			c.values[k.Path] = cloneValue(k.Default)
		}
	}
	return c
}

func cloneValue(v any) any {
	if s, ok := v.([]string); ok {
		return append([]string{}, s...)
	}
	return v
}

// Get returns the value at a dotted path, or nil if it is not set.
func (c *Config) Get(path string) any { return cloneValue(c.values[path]) }

// String returns a string (or secret reference in file form) value, or "".
func (c *Config) String(path string) string {
	switch v := c.values[path].(type) {
	case string:
		return v
	case SecretRef:
		return v.String()
	}
	return ""
}

// Bool returns a bool value, or false.
func (c *Config) Bool(path string) bool { b, _ := c.values[path].(bool); return b }

// Int returns an int value, or 0.
func (c *Config) Int(path string) int64 { n, _ := c.values[path].(int64); return n }

// Float returns a float value, or 0.
func (c *Config) Float(path string) float64 { n, _ := c.values[path].(float64); return n }

// Strings returns a copy of a string-list value.
func (c *Config) Strings(path string) []string {
	s, _ := c.values[path].([]string)
	return append([]string{}, s...)
}

// Secret returns a secret reference, if one is set at path.
func (c *Config) Secret(path string) (SecretRef, bool) {
	s, ok := c.values[path].(SecretRef)
	return s, ok
}

// Names lists the user-chosen names present under a wildcard table, for
// example Names("ai.providers") gives the configured provider names.
func (c *Config) Names(prefix string) []string {
	seen := map[string]bool{}
	for p := range c.values {
		if rest, ok := strings.CutPrefix(p, prefix+"."); ok {
			seen[strings.SplitN(rest, ".", 2)[0]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Plugin returns the free-form settings of one plugin, or nil. They are
// validated later against the plugin's manifest schema.
func (c *Config) Plugin(id string) map[string]any { return c.plugins[id] }

// PluginIDs lists configured plugin ids.
func (c *Config) PluginIDs() []string {
	out := make([]string, 0, len(c.plugins))
	for id := range c.plugins {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Error is one problem found in a config file.
type Error struct {
	File string // file name, may be empty
	Line int    // 1-based, 0 if unknown
	Key  string // dotted key, may be empty
	Msg  string
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.File != "" {
		b.WriteString(e.File)
		b.WriteString(":")
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, "%d:", e.Line)
	}
	if b.Len() > 0 {
		b.WriteString(" ")
	}
	if e.Key != "" {
		b.WriteString(e.Key)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors is the list of problems from Load or Parse. It unwraps to each
// *Error.
type Errors []*Error

func (es Errors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n")
}

// Unwrap lets errors.As find an individual *Error.
func (es Errors) Unwrap() []error {
	out := make([]error, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

// Load reads and validates a config file. A missing or empty file yields the
// defaults. Problems are returned as Errors with key and line.
func Load(path string) (*Config, error) {
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return nil, err
	}
	return parseNamed(src, path)
}

// Parse validates config text. See Load.
func Parse(src []byte) (*Config, error) { return parseNamed(src, "") }

func parseNamed(src []byte, name string) (*Config, error) {
	d, err := newDoc(src)
	if err != nil {
		return nil, tagFile(err, name)
	}
	cfg, errs := d.validate()
	if len(errs) > 0 {
		for _, e := range errs {
			e.File = name
		}
		return nil, errs
	}
	return cfg, nil
}

func tagFile(err error, name string) error {
	var es Errors
	if errors.As(err, &es) {
		for _, e := range es {
			e.File = name
		}
	}
	return err
}
