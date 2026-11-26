package api

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/store"
)

// fakeActions serves canned entries and records the limit asked for.
type fakeActions struct {
	entries []store.ActionEntry
	limit   int
}

func (f *fakeActions) RecentActions(_ context.Context, limit int) ([]store.ActionEntry, error) {
	f.limit = limit
	return f.entries[:min(limit, len(f.entries))], nil
}

func actionsClient(t *testing.T, log ActionLog) *Client {
	t.Helper()
	s := NewServer()
	Register(s, Deps{Actions: log})
	a, b := net.Pipe()
	s.ServeConn(a)
	c := NewClient(b)
	t.Cleanup(func() {
		_ = c.Close()
		s.Close()
	})
	return c
}

func TestActionsList(t *testing.T) {
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	log := &fakeActions{entries: []store.ActionEntry{
		{ID: 2, Time: at, Actor: "api", Action: "setting", Tool: "dictation.insert", Args: "auto → paste", Result: "changed", Confirmation: "auto", Reason: "config.set"},
		{ID: 1, Time: at, Actor: "owner", Action: "tool", Tool: "open_url", Args: `{"url":"https://go.dev"}`, Result: "opened", Confirmation: "auto", Reason: "open go dev"},
	}}
	c := actionsClient(t, log)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var raw json.RawMessage
	if err := c.Call(ctx, "actions.list", ActionsParams{Limit: 1}, &raw); err != nil {
		t.Fatal(err)
	}
	want := `{"actions":[{"id":2,"at":"2026-11-26T10:00:00Z","actor":"api","action":"setting","tool":"dictation.insert",` +
		`"args":"auto → paste","result":"changed","confirmation":"auto","reason":"config.set"}]}`
	if string(raw) != want {
		t.Errorf("actions.list = %s\nwant %s", raw, want)
	}

	for _, tc := range []struct{ in, want int }{{0, DefaultActionsLimit}, {5000, MaxActionsLimit}} {
		var res ActionsResult
		if err := c.Call(ctx, "actions.list", ActionsParams{Limit: tc.in}, &res); err != nil {
			t.Fatal(err)
		}
		if log.limit != tc.want || len(res.Actions) != 2 {
			t.Errorf("limit %d: store asked for %d (want %d), got %d entries", tc.in, log.limit, tc.want, len(res.Actions))
		}
	}
	if err := c.Call(ctx, "actions.list", ActionsParams{Limit: -1}, nil); code(err) != CodeInvalidParams {
		t.Errorf("negative limit: %v", err)
	}
}

func TestActionsListUnavailable(t *testing.T) {
	c := actionsClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, "actions.list", nil, nil); code(err) != CodeUnavailable {
		t.Errorf("err = %v, want unavailable", err)
	}
}

// config.set reports the change on settings.changed, for the action log.
func TestConfigSetPublishesChange(t *testing.T) {
	f := newFixture(t)
	f.mustCall("events.subscribe", SubscribeParams{Topics: []string{"settings.changed"}}, nil)
	f.mustCall("config.set", ConfigSetParams{Key: "dictation.insert", Value: json.RawMessage(`"paste"`)}, nil)
	ev := f.nextOn("settings.changed")
	want := `{"key":"dictation.insert","old":"auto","new":"paste","actor":"api","how":"auto","reason":"config.set"}`
	if string(ev.Data) != want {
		t.Errorf("settings.changed = %s, want %s", ev.Data, want)
	}
}
