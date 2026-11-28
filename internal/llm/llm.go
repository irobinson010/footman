package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/irobinson010/footman/internal/registry"
)

// Role says who wrote a message.
type Role string

// Message roles.
const (
	// User is the person speaking to Footman.
	User Role = "user"
	// Assistant is the model.
	Assistant Role = "assistant"
	// ToolResult carries the output of a tool call back to the model.
	ToolResult Role = "tool"
)

// Message is one turn of a conversation.
type Message struct {
	// Role says who wrote the message.
	Role Role
	// Text is the message text. For a ToolResult message it is the tool
	// output.
	Text string
	// ToolCalls are the tools an Assistant message asked to run.
	ToolCalls []ToolCall
	// ToolCallID links a ToolResult message to the ToolCall it answers.
	ToolCallID string
	// IsError marks a ToolResult message whose tool failed.
	IsError bool
	// Raw is the provider's own form of an Assistant message, kept when
	// Text and ToolCalls cannot carry it: a reply that used a server-side
	// tool such as web search holds blocks (the search, its encrypted
	// results, text with citations) that must go back unchanged on the next
	// call. The provider that produced it replays Raw instead of Text and
	// ToolCalls. It is opaque to everyone else and empty for ordinary
	// replies.
	Raw json.RawMessage
	// Sources are the web pages a reply with Raw drew on (its search
	// results and citations), in the order the reply used them, so the core
	// can keep their titles and addresses when it drops Raw from an old
	// exchange (ADR-0052).
	Sources []Source
}

// Source is a web page an answer drew on.
type Source struct {
	// Title is the page title, possibly empty.
	Title string
	// URL is the page address (http or https).
	URL string
}

// ToolCall is the model asking to run a tool.
type ToolCall struct {
	// ID identifies the call within the conversation.
	ID string
	// Name is the tool to run.
	Name string
	// Args are the arguments, a JSON object matching the tool's schema.
	Args json.RawMessage
}

// ToolDef describes a tool the model may call. The core builds these from
// plugins.ToolSpec, sending only the tools relevant to a request.
type ToolDef struct {
	// Name is the tool name the model uses in a ToolCall.
	Name string
	// Description tells the model what the tool does and when to use it.
	Description string
	// Schema is the JSON Schema of the arguments object.
	Schema json.RawMessage
}

// Request is one call to the model.
type Request struct {
	// Model is the provider-specific model name; empty means the provider's
	// configured default.
	Model string
	// System is the system prompt (persona and instructions). Providers that
	// support it cache the system prompt and tool definitions between calls.
	System string
	// Messages is the conversation so far, oldest first.
	Messages []Message
	// Tools the model may call; empty means none.
	Tools []ToolDef
	// MaxTokens caps the reply length; 0 means the provider default.
	MaxTokens int
	// WebSearch offers the model the provider's own web search, which runs
	// on the provider's side (ADR-0044). Providers whose Caps.WebSearch is
	// false ignore it.
	WebSearch bool
}

// EventKind says what a streamed Event carries.
type EventKind int

// Event kinds.
const (
	// TextDelta carries the next piece of reply text in Event.Text.
	TextDelta EventKind = iota + 1
	// ToolCallEvent carries one complete tool call in Event.ToolCall.
	ToolCallEvent
	// Done is the last event; it carries Event.Stop and Event.Usage.
	Done
)

// StopReason says why the model stopped.
type StopReason string

// Stop reasons.
const (
	// StopEnd means the model finished its reply.
	StopEnd StopReason = "end"
	// StopToolUse means the model is waiting for tool results.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens means the reply hit Request.MaxTokens.
	StopMaxTokens StopReason = "max_tokens"
	// StopPause means the provider paused a long server-side tool turn
	// (such as a web search). Send the reply back unchanged to continue it.
	StopPause StopReason = "pause"
)

// Usage counts tokens for cost tracking and budgets.
type Usage struct {
	// InputTokens were sent, excluding cache reads.
	InputTokens int
	// OutputTokens were generated.
	OutputTokens int
	// CacheReadTokens were served from the provider's prompt cache.
	CacheReadTokens int
	// WebSearches is how many web searches the provider ran for the call.
	WebSearches int
}

// Event is one item of a streamed reply.
type Event struct {
	// Kind says which of the fields below is set.
	Kind EventKind
	// Text is the reply text delta (TextDelta).
	Text string
	// ToolCall is a complete tool call (ToolCallEvent).
	ToolCall ToolCall
	// Stop says why the reply ended (Done).
	Stop StopReason
	// Usage is the token count for the whole call (Done).
	Usage Usage
	// Raw is the assistant message to replay, or nil when Text and
	// ToolCalls are enough (Done). See Message.Raw.
	Raw json.RawMessage
	// Sources are the pages the reply drew on (Done); see Message.Sources.
	Sources []Source
	// Untrusted is true when the reply drew on content from outside, such
	// as web search results (Done). The core then treats the task as
	// holding untrusted content (ADR-0015, ADR-0044).
	Untrusted bool
}

// Stream is a reply being generated. It is pull-based: the caller calls Recv
// until it returns io.EOF (after the Done event), and calls Close when
// finished, including early to abandon the reply. A Stream is not safe for
// concurrent use.
type Stream interface {
	// Recv blocks until the next event. It returns io.EOF after Done, or
	// another error if the call failed.
	Recv() (Event, error)
	// Close releases the stream. It is safe to call more than once.
	Close() error
}

// Caps describes what a provider and its configured model support.
type Caps struct {
	// Tools means the model can call tools.
	Tools bool
	// Vision means messages may include images (reserved for later).
	Vision bool
	// Caching means the provider caches the system prompt and tools.
	Caching bool
	// Local means the model runs on this machine, so no data leaves it.
	Local bool
	// ContextTokens is the context window size, or 0 if unknown.
	ContextTokens int
	// WebSearch means the provider can search the web for the model
	// (Request.WebSearch), on its own side.
	WebSearch bool
}

// Provider is a language model service.
type Provider interface {
	// Chat sends req and streams the reply. Cancelling ctx aborts the call;
	// the caller must still Close the stream.
	Chat(ctx context.Context, req Request) (Stream, error)
	// Capabilities reports what the provider supports.
	Capabilities() Caps
}

// Providers holds the LLM providers, keyed by the name used in config
// (`[ai] provider = "claude"`).
var Providers = registry.New[Provider]("llm provider")

// Reply is a complete reply gathered from a Stream by Collect.
type Reply struct {
	// Message is the assistant message: its text and tool calls.
	Message Message
	// Stop says why the reply ended.
	Stop StopReason
	// Usage is the token count.
	Usage Usage
	// Untrusted is true when the reply drew on content from outside; see
	// Event.Untrusted.
	Untrusted bool
}

// Collect reads s to the end, closes it, and returns the assembled reply. It
// is for callers that do not need incremental text.
func Collect(s Stream) (Reply, error) {
	defer func() { _ = s.Close() }()
	r := Reply{Message: Message{Role: Assistant}}
	var text strings.Builder
	for {
		e, err := s.Recv()
		if errors.Is(err, io.EOF) {
			r.Message.Text = text.String()
			return r, nil
		}
		if err != nil {
			r.Message.Text = text.String()
			return r, err
		}
		switch e.Kind {
		case TextDelta:
			text.WriteString(e.Text)
		case ToolCallEvent:
			r.Message.ToolCalls = append(r.Message.ToolCalls, e.ToolCall)
		case Done:
			r.Stop, r.Usage, r.Untrusted = e.Stop, e.Usage, e.Untrusted
			r.Message.Raw, r.Message.Sources = e.Raw, e.Sources
		}
	}
}
