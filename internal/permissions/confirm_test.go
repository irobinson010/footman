package permissions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/permissions"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/plugins"
)

// The broker satisfies both asking interfaces.
var (
	_ permissions.Confirmer = (*permissions.Broker)(nil)
	_ core.SettingConfirmer = (*permissions.Broker)(nil)
)

const wait = 5 * time.Second

func toolReq() permissions.Request {
	return permissions.Request{
		ID: "perm-1", Kind: permissions.KindTool, Level: plugins.Destructive,
		Title: "Allow delete_file (destructive)?", Detail: "delete_file {\"path\": \"/tmp/x\"}", Reason: "destructive actions need confirmation",
	}
}

type brokerRig struct {
	b        *permissions.Broker
	asked    *bus.Subscription[permissions.Pending]
	resolved *bus.Subscription[permissions.Resolved]
}

func newBroker(t *testing.T, n platform.Notifier, timeout time.Duration) *brokerRig {
	t.Helper()
	bb := bus.New()
	t.Cleanup(bb.Close)
	r := &brokerRig{
		b:        &permissions.Broker{Bus: bb, Notifier: n, Timeout: timeout},
		asked:    bus.Subscribe(bb, permissions.ConfirmRequested, 16),
		resolved: bus.Subscribe(bb, permissions.ConfirmResolved, 16),
	}
	t.Cleanup(r.b.Close)
	return r
}

func next[T any](t *testing.T, s *bus.Subscription[T]) T {
	t.Helper()
	select {
	case ev := <-s.C():
		return ev.Data
	case <-time.After(wait):
		t.Fatal("no event")
	}
	panic("unreachable")
}

type result struct {
	d   permissions.Decision
	err error
}

// confirm runs Confirm in the background.
func confirm(ctx context.Context, b *permissions.Broker, r permissions.Request) <-chan result {
	ch := make(chan result, 1)
	go func() {
		d, err := b.Confirm(ctx, r)
		ch <- result{d, err}
	}()
	return ch
}

func get(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(wait):
		t.Fatal("Confirm did not return")
	}
	return result{}
}

func TestBrokerAnswer(t *testing.T) {
	for _, allow := range []bool{true, false} {
		r := newBroker(t, nil, time.Minute)
		ch := confirm(t.Context(), r.b, toolReq())
		p := next(t, r.asked)
		if p.ID != "1" || p.Kind != "tool" || p.Level != "destructive" || p.ClickOnly || !strings.Contains(p.Detail, "/tmp/x") ||
			p.Expires.Sub(p.Asked) != time.Minute {
			t.Errorf("pending %+v", p)
		}
		if got := r.b.Pending(); len(got) != 1 || got[0].ID != "1" {
			t.Errorf("Pending() = %+v", got)
		}
		if err := r.b.Answer("1", allow, permissions.HowClick); err != nil {
			t.Fatal(err)
		}
		res := get(t, ch)
		if res.err != nil || res.d.Allowed != allow || res.d.How != permissions.HowClick {
			t.Errorf("allow=%v: got %+v %v", allow, res.d, res.err)
		}
		if ev := next(t, r.resolved); ev != (permissions.Resolved{ID: "1", Allowed: allow, How: permissions.HowClick}) {
			t.Errorf("resolved %+v", ev)
		}
		if len(r.b.Pending()) != 0 {
			t.Error("still pending")
		}
		if err := r.b.Answer("1", true, permissions.HowClick); !errors.Is(err, permissions.ErrNoRequest) {
			t.Errorf("second answer: %v", err)
		}
	}
}

func TestBrokerTimeout(t *testing.T) {
	r := newBroker(t, nil, 20*time.Millisecond)
	res := get(t, confirm(t.Context(), r.b, toolReq()))
	if res.err != nil || res.d.Allowed || res.d.How != permissions.HowTimeout {
		t.Errorf("got %+v %v", res.d, res.err)
	}
	next(t, r.asked)
	if ev := next(t, r.resolved); ev.Allowed || ev.How != permissions.HowTimeout {
		t.Errorf("resolved %+v", ev)
	}
	if err := r.b.Answer("1", true, permissions.HowClick); !errors.Is(err, permissions.ErrNoRequest) {
		t.Errorf("answer after expiry: %v", err)
	}
}

func TestBrokerCancel(t *testing.T) {
	r := newBroker(t, nil, time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	ch := confirm(ctx, r.b, toolReq())
	next(t, r.asked)
	cancel()
	if res := get(t, ch); !errors.Is(res.err, context.Canceled) {
		t.Errorf("got %+v %v", res.d, res.err)
	}
	if ev := next(t, r.resolved); ev.Allowed || ev.How != permissions.HowCancelled {
		t.Errorf("resolved %+v", ev)
	}
	if len(r.b.Pending()) != 0 {
		t.Error("cancelled request still pending")
	}
}

func TestBrokerClose(t *testing.T) {
	r := newBroker(t, nil, time.Minute)
	ch := confirm(t.Context(), r.b, toolReq())
	next(t, r.asked)
	r.b.Close()
	if res := get(t, ch); res.err != nil || res.d.Allowed || res.d.How != permissions.HowCancelled {
		t.Errorf("got %+v %v", res.d, res.err)
	}
	if _, err := r.b.Ask(toolReq(), func(permissions.Decision) {}); !errors.Is(err, permissions.ErrClosed) {
		t.Errorf("Ask after Close: %v", err)
	}
}

// shown waits for the i-th notification.
func shown(t *testing.T, n *pfake.Notifier, i int) platform.Notification {
	t.Helper()
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if s := n.Shown(); len(s) > i {
			return s[i]
		}
	}
	t.Fatalf("notification %d not shown", i)
	return platform.Notification{}
}

// The notification's Allow and Deny buttons answer the request as a click.
func TestBrokerNotificationButtons(t *testing.T) {
	n := &pfake.Notifier{}
	r := newBroker(t, n, time.Minute)
	for i, action := range []string{permissions.ActionAllow, permissions.ActionDeny} {
		ch := confirm(t.Context(), r.b, toolReq())
		note := shown(t, n, i)
		if note.Title != "Allow delete_file (destructive)?" || !strings.Contains(note.Body, "/tmp/x") ||
			!strings.Contains(note.Body, "footman confirm allow "+strconv.Itoa(i+1)) || len(note.Actions) != 2 ||
			note.Actions[0].ID != permissions.ActionAllow || note.Actions[1].ID != permissions.ActionDeny {
			t.Errorf("notification %+v", note)
		}
		n.Click(i, action)
		res := get(t, ch)
		if res.err != nil || res.d.Allowed != (action == permissions.ActionAllow) || res.d.How != permissions.HowClick {
			t.Errorf("%s: got %+v %v", action, res.d, res.err)
		}
	}
}

// Closing the notification without a click leaves the request answerable
// with footman confirm.
func TestBrokerNotificationDismissed(t *testing.T) {
	n := &pfake.Notifier{}
	r := newBroker(t, n, time.Minute)
	ch := confirm(t.Context(), r.b, toolReq())
	shown(t, n, 0)
	n.Click(0, "")
	time.Sleep(10 * time.Millisecond)
	if len(r.b.Pending()) != 1 {
		t.Fatal("dismissing the notification resolved the request")
	}
	if err := r.b.Answer("1", true, permissions.HowClick); err != nil {
		t.Fatal(err)
	}
	if res := get(t, ch); !res.d.Allowed {
		t.Errorf("got %+v %v", res.d, res.err)
	}
}

// A setting request is click-only: no voice answer resolves it, while a
// tool request (not click-only) may be answered by voice.
func TestBrokerClickOnly(t *testing.T) {
	r := newBroker(t, nil, time.Minute)
	type answer struct {
		allowed bool
		how     string
	}
	done := make(chan answer, 1)
	id, err := r.b.AskSetting(core.ConfirmRequest{Key: "permissions.action_log", Old: "true", New: "false", Reason: "it affects security"},
		func(allowed bool, how string) { done <- answer{allowed, how} })
	if err != nil {
		t.Fatal(err)
	}
	p := next(t, r.asked)
	if p.ID != id || !p.ClickOnly || p.Kind != "setting" || p.Detail != "permissions.action_log: true → false" || p.Title != "Change permissions.action_log?" {
		t.Errorf("pending %+v", p)
	}
	for _, allow := range []bool{true, false} {
		if err := r.b.Answer(id, allow, permissions.HowVoice); !errors.Is(err, permissions.ErrClickOnly) {
			t.Errorf("voice answer %v: %v", allow, err)
		}
	}
	if err := r.b.Answer(id, true, "telepathy"); !errors.Is(err, permissions.ErrBadHow) {
		t.Errorf("bad how: %v", err)
	}
	if len(r.b.Pending()) != 1 {
		t.Fatal("a voice answer resolved a click-only request")
	}
	if err := r.b.Answer(id, true, permissions.HowClick); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-done:
		if a != (answer{true, permissions.HowClick}) {
			t.Errorf("answer %+v", a)
		}
	case <-time.After(wait):
		t.Fatal("done not called")
	}

	// A tool request is not click-only.
	ch := confirm(t.Context(), r.b, toolReq())
	p = next(t, r.asked)
	if err := r.b.Answer(p.ID, false, permissions.HowVoice); err != nil {
		t.Fatal(err)
	}
	if res := get(t, ch); res.d.Allowed || res.d.How != permissions.HowVoice {
		t.Errorf("got %+v", res.d)
	}
}

func TestBrokerPendingOrder(t *testing.T) {
	r := newBroker(t, nil, time.Minute)
	for range 11 {
		if _, err := r.b.Ask(toolReq(), func(permissions.Decision) {}); err != nil {
			t.Fatal(err)
		}
	}
	got := r.b.Pending()
	for i, p := range got {
		if p.ID != strconv.Itoa(i+1) {
			t.Fatalf("Pending() order: %v", got)
		}
	}
}

// The gate asks the broker: a destructive call with policy confirm runs only
// after Allow, and is recorded as a click.
func TestGateWithBroker(t *testing.T) {
	n := &pfake.Notifier{}
	r := newBroker(t, n, time.Minute)
	g := &permissions.Gate{Confirmer: r.b}
	spec := plugins.ToolSpec{Name: "delete_file", Permission: plugins.Destructive}
	type out struct {
		how string
		err error
	}
	ch := make(chan out, 1)
	go func() {
		how, err := g.Decide(t.Context(), spec, json.RawMessage(`{"path":"/tmp/x"}`), false)
		ch <- out{how, err}
	}()
	p := next(t, r.asked)
	if p.ClickOnly || !strings.Contains(p.Detail, `"path": "/tmp/x"`) {
		t.Errorf("pending %+v", p)
	}
	shown(t, n, 0)
	n.Click(0, permissions.ActionAllow)
	select {
	case o := <-ch:
		if o.err != nil || o.how != permissions.HowClick {
			t.Errorf("Decide = %q, %v", o.how, o.err)
		}
	case <-time.After(wait):
		t.Fatal("Decide did not return")
	}
}
