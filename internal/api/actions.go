package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/irobinson010/footman/internal/store"
)

// ActionLog is the part of the store actions.list reads.
type ActionLog interface {
	RecentActions(ctx context.Context, limit int) ([]store.ActionEntry, error)
}

// Limits of actions.list.
const (
	DefaultActionsLimit = 20
	MaxActionsLimit     = 1000
)

// ActionsParams are the params of actions.list.
type ActionsParams struct {
	// Limit is how many entries to return, newest first (default
	// DefaultActionsLimit, at most MaxActionsLimit).
	Limit int `json:"limit,omitempty"`
}

// ActionInfo is one action-log entry. Secrets were redacted before it was
// stored.
type ActionInfo struct {
	ID int64     `json:"id"`
	At time.Time `json:"at"`
	// Actor is who: "owner" or "api".
	Actor string `json:"actor"`
	// Action is "tool", "fast_path" or "setting"; Tool names the tool,
	// rule or setting key.
	Action string `json:"action"`
	Tool   string `json:"tool"`
	// Args are the tool's arguments (JSON) or, for a setting, "old → new".
	Args   string `json:"args"`
	Result string `json:"result"`
	// Confirmation is how it was allowed or refused: "auto", "click",
	// "voice", "policy" or "timeout".
	Confirmation string `json:"confirmation"`
	// Reason is the request that led to it.
	Reason string `json:"reason"`
}

// ActionsResult is the result of actions.list.
type ActionsResult struct {
	Actions []ActionInfo `json:"actions"`
}

func (m *methods) actionsList(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p ActionsParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	if m.d.Actions == nil {
		return nil, Errorf(CodeUnavailable, "the action log is not available")
	}
	switch {
	case p.Limit < 0:
		return nil, Errorf(CodeInvalidParams, "limit must not be negative")
	case p.Limit == 0:
		p.Limit = DefaultActionsLimit
	case p.Limit > MaxActionsLimit:
		p.Limit = MaxActionsLimit
	}
	entries, err := m.d.Actions.RecentActions(ctx, p.Limit)
	if err != nil {
		return nil, err
	}
	out := ActionsResult{Actions: make([]ActionInfo, 0, len(entries))}
	for _, e := range entries {
		out.Actions = append(out.Actions, ActionInfo{
			ID: e.ID, At: e.Time, Actor: e.Actor, Action: e.Action, Tool: e.Tool,
			Args: e.Args, Result: e.Result, Confirmation: e.Confirmation, Reason: e.Reason,
		})
	}
	return out, nil
}
