// Package fake provides a deterministic, scriptable LLM provider for tests.
// Importing it registers the provider as "fake" in llm.Providers:
//
//	p, _ := llm.Providers.New("fake", registry.Map{"replies": []string{"Very good."}})
//	p.(*fake.Provider).Queue(fake.Reply{ToolCalls: []llm.ToolCall{{Name: "open_url"}}})
package fake

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/registry"
)

// Name is the registry name of the fake provider.
const Name = "fake"

func init() {
	llm.Providers.Register(Name, func(cfg registry.Config) (llm.Provider, error) { return New(cfg) })
}

// ErrNoReply is returned by Chat when no reply is queued.
var ErrNoReply = errors.New("fake llm: no reply queued")

// Reply is one scripted reply.
type Reply struct {
	// Text is streamed as one TextDelta per word (spaces kept).
	Text string
	// ToolCalls are sent after the text; if any, the stop reason is
	// StopToolUse.
	ToolCalls []llm.ToolCall
	// Err, if set, is returned by Chat instead of a stream.
	Err error
}

// Provider returns queued replies in order, one per Chat call, and records
// every request.
type Provider struct {
	mu       sync.Mutex
	caps     llm.Caps
	queue    []Reply
	requests []llm.Request
}

// New builds a Provider from cfg (key "replies": a list of reply texts to
// queue). It reports tools and caching as supported.
func New(cfg registry.Config) (*Provider, error) {
	var opts struct {
		Replies []string `json:"replies"`
	}
	if err := cfg.Decode(&opts); err != nil {
		return nil, err
	}
	p := &Provider{caps: llm.Caps{Tools: true, Caching: true, Local: true, ContextTokens: 200000}}
	for _, r := range opts.Replies {
		p.Queue(Reply{Text: r})
	}
	return p, nil
}

// Queue appends replies to be returned by later calls.
func (p *Provider) Queue(rs ...Reply) {
	p.mu.Lock()
	p.queue = append(p.queue, rs...)
	p.mu.Unlock()
}

// SetCapabilities changes what Capabilities reports.
func (p *Provider) SetCapabilities(c llm.Caps) {
	p.mu.Lock()
	p.caps = c
	p.mu.Unlock()
}

// Requests returns every request received so far, in order.
func (p *Provider) Requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.requests...)
}

// Capabilities implements llm.Provider.
func (p *Provider) Capabilities() llm.Caps {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps
}

// Chat implements llm.Provider.
func (p *Provider) Chat(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if len(p.queue) == 0 {
		return nil, ErrNoReply
	}
	r := p.queue[0]
	p.queue = p.queue[1:]
	if r.Err != nil {
		return nil, r.Err
	}

	var events []llm.Event
	for _, w := range strings.SplitAfter(r.Text, " ") {
		if w != "" {
			events = append(events, llm.Event{Kind: llm.TextDelta, Text: w})
		}
	}
	for _, tc := range r.ToolCalls {
		events = append(events, llm.Event{Kind: llm.ToolCallEvent, ToolCall: tc})
	}
	stop := llm.StopEnd
	if len(r.ToolCalls) > 0 {
		stop = llm.StopToolUse
	}
	usage := llm.Usage{InputTokens: len(req.Messages), OutputTokens: len(events)}
	events = append(events, llm.Event{Kind: llm.Done, Stop: stop, Usage: usage})
	return &stream{ctx: ctx, events: events}, nil
}

type stream struct {
	ctx    context.Context
	events []llm.Event
	closed bool
}

// Recv implements llm.Stream.
func (s *stream) Recv() (llm.Event, error) {
	if s.closed {
		return llm.Event{}, errors.New("fake llm: stream closed")
	}
	if err := s.ctx.Err(); err != nil {
		return llm.Event{}, err
	}
	if len(s.events) == 0 {
		return llm.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}

// Close implements llm.Stream.
func (s *stream) Close() error {
	s.closed = true
	return nil
}
