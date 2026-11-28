package claude_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
)

// A reply that searched lists the pages it drew on, citations first, so the
// core can keep them when it drops the raw blocks from an old exchange.
func TestWebSearchReplyListsSources(t *testing.T) {
	api := NewFakeAPI(t, searchReply("end_turn"))
	r := chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, WebSearch: true})
	want := []llm.Source{{Title: "Raleigh weather", URL: "https://weather.example/raleigh"}}
	if !reflect.DeepEqual(r.Message.Sources, want) {
		t.Errorf("sources = %v, want %v", r.Message.Sources, want)
	}
	api = NewFakeAPI(t, join(start(1, 0), textBlock(0, "Hi."), finish("end_turn", 1)))
	if r := chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}}); len(r.Message.Sources) != 0 {
		t.Errorf("an ordinary reply lists sources: %v", r.Message.Sources)
	}
}

// Carried history goes after the system prompt and the tools, so the cached
// prefix does not change: the only breakpoints are on the system prompt and
// the last tool, and none is in the messages.
func TestHistoryFollowsTheCachedPrefix(t *testing.T) {
	first := NewFakeAPI(t, searchReply("end_turn"))
	r := chat(t, first, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, WebSearch: true})
	custom := []llm.ToolDef{{Name: "open_url", Description: "Open.", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}}
	api := NewFakeAPI(t, join(start(1, 0), textBlock(0, "Sure."), finish("end_turn", 1)))
	chat(t, api, llm.Request{System: "You are Footman.", Tools: custom, WebSearch: true, Messages: []llm.Message{
		{Role: llm.User, Text: "weather"},
		r.Message,
		{Role: llm.User, Text: "open the link"},
	}})
	body := api.Bodies[0]
	if sys := body["system"].([]any)[0].(map[string]any); sys["cache_control"] == nil {
		t.Error("the system prompt lost its cache breakpoint")
	}
	if got := tools(t, api); len(got) != 2 || got[0]["cache_control"] != nil || got[1]["cache_control"] == nil {
		t.Errorf("tools = %v, want the one breakpoint on the last", got)
	}
	for i, m := range body["messages"].([]any) {
		for _, b := range m.(map[string]any)["content"].([]any) {
			if b.(map[string]any)["cache_control"] != nil {
				t.Errorf("message %d carries a cache breakpoint: the prefix would change as history grows", i)
			}
		}
	}
}
