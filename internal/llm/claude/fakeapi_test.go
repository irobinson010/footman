package claude_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// FakeAPI is an httptest server that plays back canned Messages API streams,
// one per request, and records the request bodies.
type FakeAPI struct {
	*httptest.Server
	mu     sync.Mutex
	Bodies []map[string]any
	Keys   []string
	script []string
}

// NewFakeAPI serves the given SSE bodies in order.
func NewFakeAPI(t *testing.T, script ...string) *FakeAPI {
	f := &FakeAPI{script: script}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		f.mu.Lock()
		f.Bodies = append(f.Bodies, m)
		f.Keys = append(f.Keys, r.Header.Get("X-Api-Key"))
		i := len(f.Bodies) - 1
		f.mu.Unlock()
		if r.URL.Path != "/v1/messages" || i >= len(f.script) {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"unexpected"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, f.script[i])
	}))
	t.Cleanup(f.Close)
	return f
}

func sse(events ...[2]string) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", e[0], e[1])
	}
	return b.String()
}

func start(in, cacheRead int) [2]string {
	return [2]string{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":1}}}`, in, cacheRead)}
}

func textBlock(i int, parts ...string) [][2]string {
	out := [][2]string{{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, i)}}
	for _, p := range parts {
		out = append(out, [2]string{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%q}}`, i, p)})
	}
	return append(out, [2]string{"content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i)})
}

func toolBlock(i int, id, name string, jsonParts ...string) [][2]string {
	out := [][2]string{{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, i, id, name)}}
	for _, p := range jsonParts {
		out = append(out, [2]string{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, i, p)})
	}
	return append(out, [2]string{"content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i)})
}

func finish(stop string, out int) [][2]string {
	return [][2]string{
		{"message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":%d}}`, stop, out)},
		{"message_stop", `{"type":"message_stop"}`},
	}
}

func join(parts ...any) string {
	var all [][2]string
	for _, p := range parts {
		switch v := p.(type) {
		case [2]string:
			all = append(all, v)
		case [][2]string:
			all = append(all, v...)
		}
	}
	return sse(all...)
}

// ToolUseExchange is a recorded exchange: the model asks to open YouTube,
// then, given the result, says it did.
func ToolUseExchange() []string {
	first := join(start(120, 0), textBlock(0, "Opening ", "YouTube."),
		toolBlock(1, "toolu_1", "open_url", `{"url":"https://www.`, `youtube.com"}`), finish("tool_use", 30))
	second := join(start(160, 100), textBlock(0, "Done."), finish("end_turn", 4))
	return []string{first, second}
}
