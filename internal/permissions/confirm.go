package permissions

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/plugins"
)

// Notification action IDs for a confirmation's buttons.
const (
	ActionAllow = "allow"
	ActionDeny  = "deny"
)

// HowCancelled is how a confirmation ends when the asker gave up (its task
// was cancelled) or the broker closed. It is refused.
const HowCancelled = "cancelled"

// Errors from Broker.Answer.
var (
	// ErrNoRequest: no pending confirmation has that ID (it was never
	// asked, or was already answered, expired or cancelled).
	ErrNoRequest = errors.New("no pending confirmation with that ID")
	// ErrClickOnly: the request is security-sensitive and only a click
	// may answer it; a voice answer never counts (design §29).
	ErrClickOnly = errors.New("this confirmation needs a click; a voice answer doesn't count")
	// ErrBadHow: an answer must be a click or a voice answer.
	ErrBadHow = errors.New("an answer must be a click or a voice answer")
	// ErrClosed: the broker is closed.
	ErrClosed = errors.New("confirmations are closed")
)

// Pending is a confirmation waiting for an answer, as confirm.request
// publishes it and confirm.list returns it.
type Pending struct {
	// ID is what `footman confirm allow|deny <id>` quotes.
	ID string `json:"id"`
	// Kind is "tool" or "setting"; Level the tool's permission level.
	Kind  string `json:"kind"`
	Level string `json:"level"`
	// Title, Detail and Reason are as in Request: Detail shows exactly what
	// would run or change, unredacted.
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`
	// ClickOnly: only a click (a notification button or the local API)
	// answers it.
	ClickOnly bool `json:"click_only"`
	// Asked is when it was asked; Expires when it is refused unanswered.
	Asked   time.Time `json:"asked"`
	Expires time.Time `json:"expires"`
}

// Resolved says how a confirmation ended, as confirm.resolved publishes it.
type Resolved struct {
	ID      string `json:"id"`
	Allowed bool   `json:"allowed"`
	// How is "click" or "voice" for an answer, "timeout" when nobody
	// answered in time, or "cancelled".
	How string `json:"how"`
}

// Bus topics.
var (
	// ConfirmRequested is published when a confirmation is asked.
	ConfirmRequested = bus.NewTopic[Pending]("confirm.request")
	// ConfirmResolved is published when it is answered, expires or is
	// cancelled.
	ConfirmResolved = bus.NewTopic[Resolved]("confirm.resolved")
)

// Broker holds pending confirmations and resolves them when the owner
// answers (ADR-0039). It implements Confirmer for the permission gate and
// core.SettingConfirmer for settings by voice. Each request gets a short ID,
// an expiry (Timeout, after which it is refused as "timeout") and, when there
// is a Notifier, a desktop notification with Allow and Deny buttons. The
// local API answers through Pending and Answer. Set the fields before first
// use; a Broker is then safe for concurrent use.
type Broker struct {
	// Bus receives confirm.request and confirm.resolved. May be nil.
	Bus *bus.Bus
	// Notifier shows each request with Allow and Deny buttons. May be nil;
	// a failed notification still leaves the request answerable through
	// the local API.
	Notifier platform.Notifier
	// Timeout is how long a request waits before it is refused (default
	// DefaultTimeout).
	Timeout time.Duration
	// Logf, if set, logs failures to show a notification.
	Logf func(format string, args ...any)

	mu      sync.Mutex
	seq     uint64
	pending map[string]*pendingReq
	closed  bool
}

type pendingReq struct {
	info      Pending
	done      func(Decision)
	timer     *time.Timer
	stopNotes context.CancelFunc
}

// Confirm implements Confirmer: it asks r and blocks until it is answered,
// expires (a refusal with How "timeout") or ctx ends (an error, and the
// request is withdrawn). r.ID is replaced by the broker's own.
func (b *Broker) Confirm(ctx context.Context, r Request) (Decision, error) {
	ch := make(chan Decision, 1)
	id, err := b.Ask(r, func(d Decision) { ch <- d })
	if err != nil {
		return Decision{}, err
	}
	select {
	case d := <-ch:
		return d, nil
	case <-ctx.Done():
		b.resolve(id, Decision{How: HowCancelled})
		return Decision{}, ctx.Err()
	}
}

// AskSetting implements core.SettingConfirmer. A setting request is always
// click-only.
func (b *Broker) AskSetting(r core.ConfirmRequest, done func(allowed bool, how string)) (string, error) {
	old := r.Old
	if old == "" {
		old = "(not set)"
	}
	return b.Ask(Request{
		Kind:      KindSetting,
		Level:     plugins.Act,
		Title:     "Change " + r.Key + "?",
		Detail:    fmt.Sprintf("%s: %s → %s", r.Key, old, r.New),
		Reason:    fmt.Sprintf("asked by voice, and it can't be changed by voice because %s", r.Reason),
		ClickOnly: true,
	}, func(d Decision) { done(d.Allowed, d.How) })
}

// Ask registers r, publishes confirm.request, shows the notification and
// returns the request's ID at once. done is called exactly once, from another
// goroutine, when the request is answered, expires or is cancelled.
func (b *Broker) Ask(r Request, done func(Decision)) (string, error) {
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	now := time.Now()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return "", ErrClosed
	}
	if b.pending == nil {
		b.pending = map[string]*pendingReq{}
	}
	b.seq++
	id := strconv.FormatUint(b.seq, 10)
	p := &pendingReq{
		info: Pending{
			ID: id, Kind: r.Kind, Level: r.Level.String(), Title: r.Title, Detail: r.Detail, Reason: r.Reason,
			ClickOnly: r.ClickOnly, Asked: now, Expires: now.Add(timeout),
		},
		done: done,
	}
	b.pending[id] = p
	p.timer = time.AfterFunc(timeout, func() { b.resolve(id, Decision{How: HowTimeout}) })
	var nctx context.Context
	if b.Notifier != nil {
		nctx, p.stopNotes = context.WithCancel(context.Background())
	}
	// Published under the lock so confirm.request always comes before its
	// confirm.resolved.
	if b.Bus != nil {
		bus.Publish(b.Bus, ConfirmRequested, p.info)
	}
	b.mu.Unlock()
	if nctx != nil {
		go b.notify(nctx, p.info)
	}
	return id, nil
}

// notify shows the request as a notification and answers it with the button
// clicked. Closing the notification without a click leaves it pending.
func (b *Broker) notify(ctx context.Context, p Pending) {
	body := p.Detail
	if p.Reason != "" {
		body += "\n\nWhy: " + p.Reason
	}
	body += fmt.Sprintf("\n\nOr run: footman confirm allow %s", p.ID)
	ch, err := b.Notifier.Notify(ctx, platform.Notification{
		Title: p.Title, Body: body,
		Actions: []platform.Action{{ID: ActionAllow, Label: "Allow"}, {ID: ActionDeny, Label: "Deny"}},
	})
	if err != nil {
		if ctx.Err() == nil && b.Logf != nil {
			b.Logf("confirmation %s: no notification (answer with footman confirm): %v", p.ID, err)
		}
		return
	}
	for action := range ch {
		switch action {
		case ActionAllow, ActionDeny:
			// A notification button is a physical click.
			_ = b.Answer(p.ID, action == ActionAllow, HowClick)
			return
		}
	}
}

// Answer resolves the pending request id: allow or refuse it. how is
// HowClick for a physical click (a notification button, or the local API,
// whose clients are the owner at the keyboard) or HowVoice for a spoken
// answer, which never resolves a ClickOnly request.
func (b *Broker) Answer(id string, allow bool, how string) error {
	if how != HowClick && how != HowVoice {
		return ErrBadHow
	}
	b.mu.Lock()
	p := b.pending[id]
	b.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%q: %w", id, ErrNoRequest)
	}
	if how == HowVoice && p.info.ClickOnly {
		return ErrClickOnly
	}
	if !b.resolve(id, Decision{Allowed: allow, How: how}) {
		return fmt.Errorf("%q: %w", id, ErrNoRequest)
	}
	return nil
}

// Pending returns the requests waiting for an answer, oldest first.
func (b *Broker) Pending() []Pending {
	b.mu.Lock()
	out := make([]Pending, 0, len(b.pending))
	for _, p := range b.pending {
		out = append(out, p.info)
	}
	b.mu.Unlock()
	slices.SortFunc(out, func(x, y Pending) int {
		a, _ := strconv.ParseUint(x.ID, 10, 64)
		c, _ := strconv.ParseUint(y.ID, 10, 64)
		return cmp.Compare(a, c)
	})
	return out
}

// Close refuses every pending request as cancelled and makes further asks
// fail with ErrClosed.
func (b *Broker) Close() {
	b.mu.Lock()
	b.closed = true
	ids := make([]string, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.resolve(id, Decision{How: HowCancelled})
	}
}

// resolve ends request id with d, if it is still pending: it stops the
// timer and the notification, publishes confirm.resolved and calls done.
func (b *Broker) resolve(id string, d Decision) bool {
	b.mu.Lock()
	p := b.pending[id]
	if p == nil {
		b.mu.Unlock()
		return false
	}
	delete(b.pending, id)
	p.timer.Stop()
	if b.Bus != nil {
		bus.Publish(b.Bus, ConfirmResolved, Resolved{ID: id, Allowed: d.Allowed, How: d.How})
	}
	b.mu.Unlock()
	if p.stopNotes != nil {
		p.stopNotes()
	}
	go p.done(d)
	return true
}
