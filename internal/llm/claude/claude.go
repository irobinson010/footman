// Package claude implements llm.Provider for Anthropic's Claude with the
// official Go SDK. It streams replies, supports tool use, offers Anthropic's
// server-side web search (ADR-0044) and puts prompt cache breakpoints on the
// system prompt and the tool definitions.
//
// The API key is never read from config: the caller passes a function that
// resolves it from the keychain reference (ai.providers.claude.api_key).
// The key is never logged and never appears in errors.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/irobinson010/footman/internal/llm"
)

// Name is the name of the provider in config (`ai.provider = "claude"`).
const Name = "claude"

// DefaultModel is used when neither the request nor the options name a model:
// the fast model for command handling.
const DefaultModel = anthropic.ModelClaudeHaiku5_5

// maxWebSearches caps the web searches the model may run for one request,
// to bound cost and latency. A spoken factual answer needs one or two.
const maxWebSearches = 3

const (
	defaultMaxTokens = 1024
	defaultRetries   = 2
	defaultTimeout   = 60 * time.Second

	// keyTTL is how long a resolved API key is kept in memory. Resolving it
	// is a keyring round trip (several D-Bus calls), too slow to repeat
	// before every spoken request; a key that was rotated is picked up after
	// this long, or at once after the API refuses the old one.
	keyTTL = 5 * time.Minute
	// warmEvery is the least time between two connection warm-ups.
	warmEvery = 30 * time.Second
	// defaultAPIURL is the Messages API host, which Warm connects to when
	// Options.BaseURL is empty.
	defaultAPIURL = "https://api.anthropic.com"
)

// Effort levels (config ai.providers.claude.effort). EffortDefault leaves the
// parameter out, so the model uses its own default.
const (
	EffortDefault = "default"
	// defaultEffort suits a spoken exchange: the reply is a sentence or two,
	// and Haiku 5.5 thinks first at its default (medium) effort, which
	// delays the first word (ADR-0051).
	defaultEffort = "low"
)

// effortModels are the model families that accept output_config.effort. It is
// an error on older ones (Haiku 4.5, Sonnet 4.5), so nothing is sent for a
// model not named here.
var effortModels = []string{
	"claude-haiku-5", "claude-sonnet-5", "claude-opus-5", "claude-opus-4-6", "claude-opus-4-7",
	"claude-opus-4-8", "claude-sonnet-4-6", "claude-fable-5", "claude-mythos-5",
}

// Options configure a Provider.
type Options struct {
	// APIKey resolves the API key, normally from the keychain. It is called
	// for every request, so a key stored after startup is picked up.
	APIKey func(ctx context.Context) (string, error)
	// Model is the default model (config ai.providers.claude.model).
	Model string
	// BaseURL overrides the API URL (config ai.providers.claude.url); tests
	// point it at an httptest server.
	BaseURL string
	// HTTPClient overrides the HTTP client.
	HTTPClient *http.Client
	// MaxRetries is how often the SDK retries a failed request (default 2).
	MaxRetries int
	// Timeout caps one whole request (default 60s).
	Timeout time.Duration
	// Effort is the output_config.effort level: low, medium, high, xhigh or
	// max, or EffortDefault to send none. Empty means low. It is sent only
	// to models that take it.
	Effort string
}

// Provider talks to the Claude Messages API.
type Provider struct {
	o Options

	mu       sync.Mutex
	key      string
	keyAt    time.Time
	warmedAt time.Time
}

// New builds a Provider.
func New(o Options) *Provider {
	if o.Model == "" {
		o.Model = DefaultModel
	}
	if o.MaxRetries <= 0 {
		o.MaxRetries = defaultRetries
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	return &Provider{o: o}
}

// Capabilities implements llm.Provider.
func (p *Provider) Capabilities() llm.Caps {
	return llm.Caps{Tools: true, Vision: true, Caching: true, WebSearch: true, ContextTokens: 200000}
}

// Chat implements llm.Provider.
func (p *Provider) Chat(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if p.o.APIKey == nil {
		return nil, errors.New("claude: no API key configured (set ai.providers.claude.api_key = \"keychain:claude\")")
	}
	key, err := p.apiKey(ctx)
	if err != nil {
		return nil, err
	}
	opts := []option.RequestOption{
		option.WithAPIKey(key),
		option.WithMaxRetries(p.o.MaxRetries),
		option.WithRequestTimeout(p.o.Timeout),
	}
	if p.o.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(p.o.BaseURL))
	}
	if p.o.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(p.o.HTTPClient))
	}
	params, err := p.params(req)
	if err != nil {
		return nil, err
	}
	client := anthropic.NewClient(opts...)
	ctx, cancel := context.WithCancel(ctx)
	return &stream{s: client.Messages.NewStreaming(ctx, params), cancel: cancel, blocks: map[int64]*toolBlock{}, onAuth: p.dropKey}, nil
}

// apiKey returns the API key, from memory when it was resolved less than
// keyTTL ago. A failure is never kept, so a key stored later is found.
func (p *Provider) apiKey(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.key != "" && time.Since(p.keyAt) < keyTTL {
		k := p.key
		p.mu.Unlock()
		return k, nil
	}
	p.mu.Unlock()
	key, err := p.o.APIKey(ctx)
	if err != nil {
		return "", fmt.Errorf("claude: API key: %w", err)
	}
	p.mu.Lock()
	p.key, p.keyAt = key, time.Now()
	p.mu.Unlock()
	return key, nil
}

// dropKey forgets the kept key, after the API refused it.
func (p *Provider) dropKey() {
	p.mu.Lock()
	p.key, p.keyAt = "", time.Time{}
	p.mu.Unlock()
}

// Warm gets the provider ready for a request that is about to come (the
// wake word was just heard): it resolves the API key, which is a slow keyring
// round trip, and opens the connection to the API, so the TCP and TLS
// handshakes are not part of the request. It sends no credentials and no
// content, only a HEAD request to the API host, and does nothing more than
// once in warmEvery. It never fails; the request itself reports problems.
func (p *Provider) Warm(ctx context.Context) {
	if p.o.APIKey == nil {
		return
	}
	if _, err := p.apiKey(ctx); err != nil {
		return
	}
	p.mu.Lock()
	if time.Since(p.warmedAt) < warmEvery {
		p.mu.Unlock()
		return
	}
	p.warmedAt = time.Now()
	p.mu.Unlock()
	url := p.o.BaseURL
	if url == "" {
		url = defaultAPIURL
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return
	}
	c := p.o.HTTPClient
	if c == nil {
		c = http.DefaultClient
	}
	if resp, err := c.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// effortFor is the effort level to send for model: the configured one
// (default low), or none for a model that does not take the parameter or
// when the setting is EffortDefault.
func (p *Provider) effortFor(model string) anthropic.OutputConfigEffort {
	e := strings.ToLower(strings.TrimSpace(p.o.Effort))
	switch e {
	case "":
		e = defaultEffort
	case EffortDefault:
		return ""
	}
	for _, m := range effortModels {
		if strings.HasPrefix(model, m) {
			return anthropic.OutputConfigEffort(e)
		}
	}
	return ""
}

func (p *Provider) params(req llm.Request) (anthropic.MessageNewParams, error) {
	model := req.Model
	if model == "" {
		model = p.o.Model
	}
	max := req.MaxTokens
	if max <= 0 {
		max = defaultMaxTokens
	}
	params := anthropic.MessageNewParams{Model: model, MaxTokens: int64(max)}
	params.OutputConfig.Effort = p.effortFor(model)
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}}
	}
	for _, t := range req.Tools {
		tp := anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description)}
		if len(t.Schema) > 0 {
			var s struct {
				Properties any      `json:"properties"`
				Required   []string `json:"required"`
			}
			if err := json.Unmarshal(t.Schema, &s); err != nil {
				return params, fmt.Errorf("claude: tool %s: bad schema: %w", t.Name, err)
			}
			tp.InputSchema = anthropic.ToolInputSchemaParam{Properties: s.Properties, Required: s.Required}
		}
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	if req.WebSearch {
		params.Tools = append(params.Tools, webSearchTool())
	}
	// One breakpoint on the last tool caches all the tool definitions.
	if n := len(params.Tools); n > 0 {
		switch last := params.Tools[n-1]; {
		case last.OfWebSearchTool20260209 != nil:
			last.OfWebSearchTool20260209.CacheControl = anthropic.NewCacheControlEphemeralParam()
		case last.OfTool != nil:
			last.OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
	}
	msgs, err := messages(req.Messages)
	if err != nil {
		return params, err
	}
	params.Messages = msgs
	return params, nil
}

// webSearchTool is Anthropic's server-side web search: the API runs the
// searches and hands the model the results, so there is nothing to execute
// here. The 20260209 version defaults to dynamic filtering, which runs the
// search from inside code execution; a spoken answer does not need it (it adds
// latency, and its blocks nest), so the tool is called directly. Failed
// searches count against max_uses.
func webSearchTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfWebSearchTool20260209: &anthropic.WebSearchTool20260209Param{
		MaxUses:        anthropic.Int(maxWebSearches),
		AllowedCallers: []string{"direct"},
	}}
}

// messages converts the conversation. Consecutive tool results are merged
// into one user message, as the API requires.
func messages(in []llm.Message) ([]anthropic.MessageParam, error) {
	var out []anthropic.MessageParam
	var results []anthropic.ContentBlockParamUnion
	flush := func() {
		if len(results) > 0 {
			out = append(out, anthropic.NewUserMessage(results...))
			results = nil
		}
	}
	for _, m := range in {
		if m.Role != llm.ToolResult {
			flush()
		}
		switch m.Role {
		case llm.User:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Text)))
		case llm.Assistant:
			if len(m.Raw) > 0 {
				// A reply that used a server tool goes back exactly as it
				// came, with its encrypted search results and citations.
				raw, err := json.Marshal(struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}{"assistant", m.Raw})
				if err != nil {
					return nil, fmt.Errorf("claude: assistant message: %w", err)
				}
				out = append(out, param.Override[anthropic.MessageParam](json.RawMessage(raw)))
				continue
			}
			var blocks []anthropic.ContentBlockParamUnion
			if m.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Text))
			}
			for _, c := range m.ToolCalls {
				var input any = map[string]any{}
				if len(c.Args) > 0 {
					if err := json.Unmarshal(c.Args, &input); err != nil {
						return nil, fmt.Errorf("claude: tool call %s: bad args: %w", c.Name, err)
					}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(c.ID, input, c.Name))
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case llm.ToolResult:
			results = append(results, anthropic.NewToolResultBlock(m.ToolCallID, m.Text, m.IsError))
		default:
			return nil, fmt.Errorf("claude: unknown role %q", m.Role)
		}
	}
	flush()
	return out, nil
}

type toolBlock struct {
	id, name string
	json     []byte
}

// stream adapts the SDK's SSE stream to llm.Stream.
type stream struct {
	s interface {
		Next() bool
		Current() anthropic.MessageStreamEventUnion
		Err() error
		Close() error
	}
	cancel  context.CancelFunc
	blocks  map[int64]*toolBlock
	acc     anthropic.Message // the whole reply, for replaying server-tool blocks
	server  bool              // the reply used a server tool (web search)
	outside bool              // the reply holds content from outside (search results)
	last    string            // the last text sent, to keep words apart across blocks
	pending []llm.Event
	usage   llm.Usage
	stop    llm.StopReason
	done    bool
	onAuth  func() // called when the API refuses the key
}

func (s *stream) Recv() (llm.Event, error) {
	for {
		if len(s.pending) > 0 {
			e := s.pending[0]
			s.pending = s.pending[1:]
			return e, nil
		}
		if s.done {
			return llm.Event{}, io.EOF
		}
		if !s.s.Next() {
			if err := s.s.Err(); err != nil {
				var ae *anthropic.Error
				if errors.As(err, &ae) && (ae.StatusCode == http.StatusUnauthorized || ae.StatusCode == http.StatusForbidden) && s.onAuth != nil {
					s.onAuth()
				}
				return llm.Event{}, fmt.Errorf("claude: %w", err)
			}
			return llm.Event{}, io.ErrUnexpectedEOF
		}
		if err := s.handle(s.s.Current()); err != nil {
			return llm.Event{}, fmt.Errorf("claude: %w", err)
		}
	}
}

func (s *stream) handle(ev anthropic.MessageStreamEventUnion) error {
	if err := s.acc.Accumulate(ev); err != nil {
		return err
	}
	switch ev.Type {
	case "message_start":
		u := ev.Message.Usage
		s.usage.InputTokens = int(u.InputTokens + u.CacheCreationInputTokens)
		s.usage.CacheReadTokens = int(u.CacheReadInputTokens)
		s.usage.OutputTokens = int(u.OutputTokens)
	case "content_block_start":
		switch ev.ContentBlock.Type {
		case "tool_use":
			s.blocks[ev.Index] = &toolBlock{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
		case "server_tool_use":
			s.server = true
			if s.last != "" && !strings.HasSuffix(s.last, " ") {
				// "I'll look that up." and what follows the search are
				// separate blocks; keep their words apart.
				s.pending = append(s.pending, llm.Event{Kind: llm.TextDelta, Text: " "})
				s.last = " "
			}
		case "web_search_tool_result":
			s.server, s.outside = true, true
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				s.last = ev.Delta.Text
				s.pending = append(s.pending, llm.Event{Kind: llm.TextDelta, Text: ev.Delta.Text})
			}
		case "input_json_delta":
			if b := s.blocks[ev.Index]; b != nil {
				b.json = append(b.json, ev.Delta.PartialJSON...)
			}
		}
	case "content_block_stop":
		if b := s.blocks[ev.Index]; b != nil {
			delete(s.blocks, ev.Index)
			args := json.RawMessage(b.json)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			s.pending = append(s.pending, llm.Event{Kind: llm.ToolCallEvent, ToolCall: llm.ToolCall{ID: b.id, Name: b.name, Args: args}})
		}
	case "message_delta":
		u := ev.Usage
		if u.OutputTokens > 0 {
			s.usage.OutputTokens = int(u.OutputTokens)
		}
		if u.ServerToolUse.WebSearchRequests > 0 {
			s.usage.WebSearches = int(u.ServerToolUse.WebSearchRequests)
		}
		switch ev.Delta.StopReason {
		case anthropic.StopReasonToolUse:
			s.stop = llm.StopToolUse
		case anthropic.StopReasonMaxTokens:
			s.stop = llm.StopMaxTokens
		case anthropic.StopReasonPauseTurn:
			s.stop = llm.StopPause
		default:
			s.stop = llm.StopEnd
		}
	case "message_stop":
		if s.stop == "" {
			s.stop = llm.StopEnd
		}
		s.done = true
		done := llm.Event{Kind: llm.Done, Stop: s.stop, Usage: s.usage, Untrusted: s.outside}
		if s.server {
			done.Raw = s.replay()
			done.Sources = sources(done.Raw)
		}
		s.pending = append(s.pending, done)
	}
	return nil
}

// replay is the reply's content blocks as the API sent them (with the
// streamed deltas applied), for sending back unchanged on the next call.
func (s *stream) replay() json.RawMessage {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(s.acc.RawJSON()), &m); err != nil || len(m.Content) == 0 {
		return nil
	}
	return m.Content
}

// maxSources caps how many pages sources returns.
const maxSources = 10

// sources lists the web pages in a reply's content blocks: the pages its
// text cited, in the order they were cited, then the other search results.
// Only http and https addresses are kept, each once.
func sources(content json.RawMessage) []llm.Source {
	var blocks []struct {
		Type      string `json:"type"`
		Citations []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"citations"`
		// A web_search_tool_result's content is a list of results, or an
		// error object.
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	var cited, found []llm.Source
	seen := map[string]bool{}
	add := func(dst *[]llm.Source, url, title string) {
		if seen[url] || len(seen) >= maxSources || (!strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://")) {
			return
		}
		seen[url] = true
		*dst = append(*dst, llm.Source{Title: strings.TrimSpace(title), URL: url})
	}
	// Citations first, so the order is the one the answer used.
	for _, b := range blocks {
		for _, c := range b.Citations {
			add(&cited, c.URL, c.Title)
		}
	}
	for _, b := range blocks {
		if b.Type != "web_search_tool_result" {
			continue
		}
		var results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		if json.Unmarshal(b.Content, &results) != nil {
			continue
		}
		for _, r := range results {
			add(&found, r.URL, r.Title)
		}
	}
	return append(cited, found...)
}

func (s *stream) Close() error {
	s.cancel()
	return s.s.Close()
}
