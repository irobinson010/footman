package permissions

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/store"
)

// Kinds of action-log entry (store.ActionEntry.Action).
const (
	ActionTool     = "tool"
	ActionFastPath = "fast_path"
	ActionSetting  = "setting"
)

// maxField caps the length of the args and result recorded per entry.
const maxField = 2000

// recentTurns is how many turns the log remembers the request text and
// speaker of.
const recentTurns = 64

// Appender is the part of the store the action log writes to.
type Appender interface {
	AppendAction(ctx context.Context, e store.ActionEntry) (int64, error)
}

// Log records every action into the action log (design §29, ADR-0038): each
// tool call, fast-path action and setting change, with who did it, what
// (secrets redacted), the result, how it was confirmed and why. It reads the
// bus rather than being called from the pipeline.
type Log struct {
	// Store receives the entries.
	Store Appender
	// Config returns the current config; permissions.action_log off
	// records nothing. Nil means the defaults (on).
	Config func() *config.Config
	// Logf reports write failures. Nil means they are dropped.
	Logf func(format string, args ...any)
}

// turnInfo is what the log remembers about a turn.
type turnInfo struct {
	spoken bool   // a transcript was published: the owner spoke
	text   string // the routed request text
}

// Watch subscribes to b at once and records actions in the background until
// the bus closes or stop is called. stop waits for the events already
// received to be written.
func (l *Log) Watch(b *bus.Bus) (stop func()) {
	sub := bus.SubscribeAll(b, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		turns := map[uint64]*turnInfo{}
		for ev := range sub.C() {
			l.handle(turns, ev.Data)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(sub.Close)
		<-done
	}
}

func (l *Log) handle(turns map[uint64]*turnInfo, data any) {
	turn := func(id uint64) *turnInfo {
		t := turns[id]
		if t == nil {
			t = &turnInfo{}
			turns[id] = t
			if len(turns) > recentTurns {
				oldest := id
				for k := range turns {
					oldest = min(oldest, k)
				}
				delete(turns, oldest)
			}
		}
		return t
	}
	actor := func(id uint64) string {
		if t := turns[id]; t != nil && !t.spoken && t.text != "" {
			// Routed without being transcribed: text from a socket
			// client (say.text).
			return core.ActorAPI
		}
		return core.ActorOwner
	}
	reason := func(id uint64) string {
		if t := turns[id]; t != nil {
			return t.text
		}
		return ""
	}
	switch d := data.(type) {
	case core.Transcript:
		turn(d.Turn).spoken = true
	case core.Routed:
		turn(d.Turn).text = d.Route.Text
	case core.ToolRan:
		res := d.Result
		if d.IsError {
			res = "error: " + res
		}
		l.append(store.ActionEntry{
			Actor: actor(d.Turn), Action: ActionTool, Tool: d.Name,
			Args: RedactJSON(d.Args), Result: res, Confirmation: d.Confirmation, Reason: reason(d.Turn),
		})
	case core.FastPathRan:
		res := d.Reply
		if d.Err != "" {
			res = "error: " + d.Err
		}
		l.append(store.ActionEntry{
			Actor: actor(d.Turn), Action: ActionFastPath, Tool: d.Rule,
			Result: res, Confirmation: d.Confirmation, Reason: reason(d.Turn),
		})
	case core.SettingChange:
		old, cur := d.Old, d.New
		last := d.Key[strings.LastIndexByte(d.Key, '.')+1:]
		if secretSetting(d.Key) || secretField(last) {
			old, cur = redactSet(old), redactSet(cur)
		}
		l.append(store.ActionEntry{
			Actor: d.Actor, Action: ActionSetting, Tool: d.Key,
			Args: show(old) + " → " + show(cur), Result: "changed", Confirmation: d.How, Reason: d.Reason,
		})
	}
}

// redactSet redacts a secret setting's value, keeping whether it was set.
func redactSet(v string) string {
	if v == "" {
		return ""
	}
	return Redacted
}

func show(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

// append redacts and caps the free-text fields and writes e, unless the
// action log is off.
func (l *Log) append(e store.ActionEntry) {
	cfg := defaults
	if l.Config != nil {
		if c := l.Config(); c != nil {
			cfg = c
		}
	}
	if !cfg.Bool("permissions.action_log") {
		return
	}
	e.Args = truncate(Redact(e.Args))
	e.Result = truncate(Redact(e.Result))
	e.Reason = truncate(Redact(e.Reason))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.Store.AppendAction(ctx, e); err != nil && l.Logf != nil {
		l.Logf("action log: %v", err)
	}
}

func truncate(s string) string {
	if len(s) <= maxField {
		return s
	}
	cut := maxField
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
