package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
)

func provider(api *FakeAPI, keyErr error) *claude.Provider {
	return claude.New(claude.Options{
		BaseURL:    api.URL,
		MaxRetries: 1,
		APIKey: func(context.Context) (string, error) {
			if keyErr != nil {
				return "", keyErr
			}
			return "sk-test-secret", nil
		},
	})
}

func TestStreamsTextToolCallAndUsage(t *testing.T) {
	api := NewFakeAPI(t, ToolUseExchange()[0])
	s, err := provider(api, nil).Chat(context.Background(), llm.Request{
		System:   "You are Footman.",
		Messages: []llm.Message{{Role: llm.User, Text: "open youtube"}},
		Tools: []llm.ToolDef{
			{Name: "open_url", Description: "Open a URL.", Schema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)},
			{Name: "open_app", Description: "Open an app.", Schema: json.RawMessage(`{"type":"object","properties":{"app":{"type":"string"}}}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := llm.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Text != "Opening YouTube." || r.Stop != llm.StopToolUse {
		t.Errorf("reply = %q stop %q", r.Message.Text, r.Stop)
	}
	if len(r.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", r.Message.ToolCalls)
	}
	c := r.Message.ToolCalls[0]
	if c.ID != "toolu_1" || c.Name != "open_url" || string(c.Args) != `{"url":"https://www.youtube.com"}` {
		t.Errorf("call = %+v (%s)", c, c.Args)
	}
	if r.Usage != (llm.Usage{InputTokens: 120, OutputTokens: 30}) {
		t.Errorf("usage = %+v", r.Usage)
	}

	if api.Keys[0] != "sk-test-secret" {
		t.Errorf("api key header = %q", api.Keys[0])
	}
	body := api.Bodies[0]
	if body["model"] != claude.DefaultModel || body["stream"] != true {
		t.Errorf("model/stream = %v/%v", body["model"], body["stream"])
	}
	sys := body["system"].([]any)[0].(map[string]any)
	if sys["text"] != "You are Footman." || sys["cache_control"] == nil {
		t.Errorf("system block = %v", sys)
	}
	tools := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %v", tools)
	}
	if tools[0].(map[string]any)["cache_control"] != nil || tools[1].(map[string]any)["cache_control"] == nil {
		t.Errorf("want one cache breakpoint on the last tool: %v", tools)
	}
}

func TestToolResultRoundTrip(t *testing.T) {
	ex := ToolUseExchange()
	api := NewFakeAPI(t, ex[1])
	s, err := provider(api, nil).Chat(context.Background(), llm.Request{
		Model: "claude-sonnet-5-5",
		Messages: []llm.Message{
			{Role: llm.User, Text: "open youtube"},
			{Role: llm.Assistant, Text: "Opening.", ToolCalls: []llm.ToolCall{
				{ID: "toolu_1", Name: "open_url", Args: json.RawMessage(`{"url":"https://www.youtube.com"}`)},
				{ID: "toolu_2", Name: "open_app", Args: json.RawMessage(`{"app":"x"}`)},
			}},
			{Role: llm.ToolResult, ToolCallID: "toolu_1", Text: "opened"},
			{Role: llm.ToolResult, ToolCallID: "toolu_2", Text: "boom", IsError: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := llm.Collect(s)
	if err != nil || r.Message.Text != "Done." || r.Stop != llm.StopEnd {
		t.Fatalf("reply = %+v, %v", r, err)
	}
	if r.Usage.CacheReadTokens != 100 || r.Usage.InputTokens != 160 {
		t.Errorf("usage = %+v", r.Usage)
	}
	body := api.Bodies[0]
	if body["model"] != "claude-sonnet-5-5" {
		t.Errorf("model = %v", body["model"])
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want tool results merged into one user message", len(msgs))
	}
	last := msgs[2].(map[string]any)
	blocks := last["content"].([]any)
	if last["role"] != "user" || len(blocks) != 2 {
		t.Fatalf("last = %v", last)
	}
	b0, b1 := blocks[0].(map[string]any), blocks[1].(map[string]any)
	if b0["type"] != "tool_result" || b0["tool_use_id"] != "toolu_1" || b1["is_error"] != true {
		t.Errorf("tool results = %v", blocks)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 || asst[1].(map[string]any)["type"] != "tool_use" {
		t.Errorf("assistant blocks = %v", asst)
	}
}

func TestKeyErrorsNeverLeakKey(t *testing.T) {
	api := NewFakeAPI(t)
	_, err := provider(api, errors.New("not found")).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err = %v", err)
	}
	// An API error (the fake rejects the unscripted request) must not echo the key.
	s, err := provider(api, nil).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}})
	if err == nil {
		_, err = llm.Collect(s)
	}
	if err == nil || strings.Contains(err.Error(), "sk-test-secret") {
		t.Errorf("err = %v", err)
	}
	if _, err := claude.New(claude.Options{}).Chat(context.Background(), llm.Request{}); err == nil {
		t.Error("want error without a key resolver")
	}
}
