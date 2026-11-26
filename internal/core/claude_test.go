package core_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
)

// A recorded tool-use exchange against a fake Messages API drives an
// "open youtube" tool call through the real Claude provider and the agent.
func TestClaudeAgentOpensYouTube(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	events := func(parts ...string) string {
		var b strings.Builder
		for _, p := range parts {
			typ := p[strings.Index(p, `"type":"`)+8:]
			typ = typ[:strings.IndexByte(typ, '"')]
			fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", typ, p)
		}
		return b.String()
	}
	msgStart := `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":50,"output_tokens":1}}}`
	script := []string{
		events(msgStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"open_url","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"url\":\"https://www.youtube.com\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
			`{"type":"message_stop"}`),
		events(msgStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"YouTube is open."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		i := len(bodies)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if i < len(script) {
			_, _ = io.WriteString(w, script[i])
		}
	}))
	defer srv.Close()

	tracker := llm.NewTracker(nil)
	p := llm.Metered(claude.New(claude.Options{
		BaseURL: srv.URL,
		APIKey:  func(context.Context) (string, error) { return "sk-test", nil },
	}), tracker, claude.DefaultModel)
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.LLM = p })

	r.submit(core.TriggerCommand, "bring up the video site")

	if got := r.launcher().URLs(); len(got) != 1 || got[0] != "https://www.youtube.com" {
		t.Fatalf("opened URLs = %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("API calls = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], `"open_url"`) || !strings.Contains(bodies[1], `"tool_use_id":"toolu_1"`) {
		t.Errorf("bodies:\n%s\n%s", bodies[0], bodies[1])
	}
	if u := tracker.Total(claude.DefaultModel); u.InputTokens != 100 || u.OutputTokens != 25 {
		t.Errorf("usage = %+v", u)
	}
}
