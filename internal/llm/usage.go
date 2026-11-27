package llm

import (
	"context"
	"sync"
)

// UsageFunc receives the token usage of one finished model call. Cost caps
// hook in here.
type UsageFunc func(model string, u Usage)

// Tracker adds up usage per model. It is safe for concurrent use.
type Tracker struct {
	mu     sync.Mutex
	totals map[string]Usage
	hook   UsageFunc
}

// NewTracker returns a Tracker that also calls hook (if not nil) for each
// recorded call.
func NewTracker(hook UsageFunc) *Tracker {
	return &Tracker{totals: map[string]Usage{}, hook: hook}
}

// Record adds u to the total for model.
func (t *Tracker) Record(model string, u Usage) {
	t.mu.Lock()
	tot := t.totals[model]
	tot.InputTokens += u.InputTokens
	tot.OutputTokens += u.OutputTokens
	tot.CacheReadTokens += u.CacheReadTokens
	tot.WebSearches += u.WebSearches
	t.totals[model] = tot
	hook := t.hook
	t.mu.Unlock()
	if hook != nil {
		hook(model, u)
	}
}

// Total returns the usage recorded for model.
func (t *Tracker) Total(model string) Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totals[model]
}

// Metered wraps p so every finished call is recorded on t. The model is the
// request's model, or defaultModel when the request leaves it empty.
func Metered(p Provider, t *Tracker, defaultModel string) Provider {
	return &metered{Provider: p, t: t, model: defaultModel}
}

type metered struct {
	Provider
	t     *Tracker
	model string
}

func (m *metered) Chat(ctx context.Context, req Request) (Stream, error) {
	s, err := m.Provider.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = m.model
	}
	return &meteredStream{Stream: s, t: m.t, model: model}, nil
}

type meteredStream struct {
	Stream
	t     *Tracker
	model string
	once  sync.Once
}

func (s *meteredStream) Recv() (Event, error) {
	e, err := s.Stream.Recv()
	if err == nil && e.Kind == Done {
		s.once.Do(func() { s.t.Record(s.model, e.Usage) })
	}
	return e, err
}
