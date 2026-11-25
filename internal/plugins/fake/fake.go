// Package fake provides a deterministic, scriptable tool for tests. Importing
// it registers the tool as "fake" in plugins.Tools:
//
//	t, _ := plugins.Tools.New("fake", registry.Map{"name": "send_email", "permission": "outward"})
//	t.(*fake.Tool).Queue(plugins.Result{Text: "sent"})
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/irobinson010/footman/internal/plugins"
	"github.com/irobinson010/footman/internal/registry"
)

// Name is the registry name of the fake tool.
const Name = "fake"

func init() {
	plugins.Tools.Register(Name, func(cfg registry.Config) (plugins.Tool, error) { return New(cfg) })
}

// ErrNoResult is returned by Run when no result is queued.
var ErrNoResult = errors.New("fake tool: no result queued")

// Tool returns queued results in order, one per Run call, and records the
// arguments of every call.
type Tool struct {
	mu    sync.Mutex
	spec  plugins.ToolSpec
	queue []plugins.Result
	calls []json.RawMessage
}

// New builds a Tool from cfg. Keys: "name" (default "fake"), "description",
// "permission" (default "read"), "phrases" and "results" (a list of result
// texts to queue).
func New(cfg registry.Config) (*Tool, error) {
	opts := struct {
		plugins.ToolSpec
		Results []string `json:"results"`
	}{ToolSpec: plugins.ToolSpec{Name: Name, Description: "A fake tool for tests.", Permission: plugins.Read}}
	if err := cfg.Decode(&opts); err != nil {
		return nil, err
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	t := &Tool{spec: opts.ToolSpec}
	for _, r := range opts.Results {
		t.Queue(plugins.Result{Text: r})
	}
	return t, nil
}

// Queue appends results to be returned by later calls.
func (t *Tool) Queue(rs ...plugins.Result) {
	t.mu.Lock()
	t.queue = append(t.queue, rs...)
	t.mu.Unlock()
}

// Calls returns the arguments of every call so far, in order.
func (t *Tool) Calls() []json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]json.RawMessage(nil), t.calls...)
}

// Spec implements plugins.Tool.
func (t *Tool) Spec() plugins.ToolSpec { return t.spec }

// Run implements plugins.Tool.
func (t *Tool) Run(ctx context.Context, args json.RawMessage) (plugins.Result, error) {
	if err := ctx.Err(); err != nil {
		return plugins.Result{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, append(json.RawMessage(nil), args...))
	if len(t.queue) == 0 {
		return plugins.Result{}, ErrNoResult
	}
	r := t.queue[0]
	t.queue = t.queue[1:]
	return r, nil
}
