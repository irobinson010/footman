package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/irobinson010/footman/internal/registry"
)

// Permission is how much a tool can affect the world. It decides whether the
// permission gate runs a call straight away, asks for confirmation or refuses
// (config `[permissions]`, design §29).
//
// The zero value is not a valid permission, so a ToolSpec that forgets to set
// one is caught by Validate rather than silently treated as Read.
type Permission int

// Permission levels, as named in plugin manifests and config.
const (
	// Read only looks: answering questions, reading windows or files.
	Read Permission = iota + 1
	// Act changes local state in a way that is easy to undo: opening an app,
	// changing the volume, starting a timer.
	Act
	// Destructive deletes or overwrites something that cannot easily be
	// recovered.
	Destructive
	// Outward sends something outside the machine on the user's behalf: a
	// message, an email, a post, a purchase.
	Outward
)

var permissionNames = [...]string{Read: "read", Act: "act", Destructive: "destructive", Outward: "outward"}

// String returns the manifest name of p ("read", "act", "destructive" or
// "outward").
func (p Permission) String() string {
	if p.Valid() {
		return permissionNames[p]
	}
	return fmt.Sprintf("Permission(%d)", int(p))
}

// Valid reports whether p is one of the four levels.
func (p Permission) Valid() bool { return p >= Read && p <= Outward }

// ParsePermission parses a manifest or config name.
func ParsePermission(s string) (Permission, error) {
	for p := Read; p <= Outward; p++ {
		if permissionNames[p] == s {
			return p, nil
		}
	}
	return 0, fmt.Errorf("plugins: unknown permission %q (want read, act, destructive or outward)", s)
}

// MarshalText implements encoding.TextMarshaler.
func (p Permission) MarshalText() ([]byte, error) {
	if !p.Valid() {
		return nil, fmt.Errorf("plugins: invalid permission %d", int(p))
	}
	return []byte(p.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *Permission) UnmarshalText(b []byte) error {
	v, err := ParsePermission(string(b))
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// ToolSpec describes a tool: what the AI sees and what the permission gate
// checks.
type ToolSpec struct {
	// Name is unique across all loaded tools, such as "open_url".
	Name string `json:"name"`
	// Description tells the AI what the tool does and when to use it.
	Description string `json:"description"`
	// Schema is the JSON Schema of the arguments object.
	Schema json.RawMessage `json:"schema,omitempty"`
	// Permission is the tool's level.
	Permission Permission `json:"permission"`
	// Phrases are optional simple-tier fast-path triggers, such as
	// "open {site}", matched locally without the AI.
	Phrases []string `json:"phrases,omitempty"`
}

// Validate reports whether the spec has a name and a valid permission.
func (s ToolSpec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("plugins: tool spec has no name")
	}
	if !s.Permission.Valid() {
		return fmt.Errorf("plugins: tool %q has invalid permission %v", s.Name, s.Permission)
	}
	return nil
}

// Result is the output of one tool call.
type Result struct {
	// Text is the output shown to the AI and, where useful, the user.
	Text string `json:"text"`
	// Data is optional structured output.
	Data json.RawMessage `json:"data,omitempty"`
	// IsError marks a failure the AI should see and can recover from (bad
	// arguments, not found). Failures of the tool itself are returned as Go
	// errors instead.
	IsError bool `json:"is_error,omitempty"`
	// Untrusted marks output that came from outside (web pages, messages,
	// files, plugin output). Once untrusted content is in a task's context,
	// every outward or destructive action in that task needs confirmation
	// (design §29).
	Untrusted bool `json:"untrusted,omitempty"`
}

// Tool is one action Footman can take: a built-in tool or one exposed by a
// plugin.
type Tool interface {
	// Spec describes the tool. It must not change while the tool is loaded.
	Spec() ToolSpec
	// Run executes one call. args is a JSON object matching Spec().Schema.
	// Run is only called after the permission gate has allowed the call.
	Run(ctx context.Context, args json.RawMessage) (Result, error)
}

// Tools holds the built-in tools compiled into the binary, keyed by tool name.
// External plugins are loaded by the plugin host, not through this registry.
var Tools = registry.New[Tool]("tool")
