package claude_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
)

const (
	searchResult = `{"type":"web_search_result","url":"https://weather.example/raleigh","title":"Raleigh weather","encrypted_content":"ENCRYPTED-RESULT==","page_age":"1 hour ago"}`
	citation     = `{"type":"web_search_result_location","url":"https://weather.example/raleigh","title":"Raleigh weather","encrypted_index":"ENCRYPTED-INDEX==","cited_text":"62 and sunny"}`
)

// searchReply is a recorded web search turn: a lead-in, the search, its
// results (which arrive whole) and an answer with a citation.
func searchReply(stop string) string {
	result := [2]string{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[%s]}}`, searchResult)}
	resultStop := [2]string{"content_block_stop", `{"type":"content_block_stop","index":2}`}
	server := [][2]string{{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`}}
	for _, p := range []string{`{"query":"raleigh `, `weather today"}`} {
		server = append(server, [2]string{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%q}}`, p)})
	}
	server = append(server, [2]string{"content_block_stop", `{"type":"content_block_stop","index":1}`})
	answer := textBlock(3, "It is 62 and ", "sunny.")
	cite := [2]string{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":3,"delta":{"type":"citations_delta","citation":%s}}`, citation)}
	answer = append(answer[:len(answer)-1], cite, answer[len(answer)-1])
	end := [][2]string{
		{"message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":40,"server_tool_use":{"web_search_requests":1}}}`, stop)},
		{"message_stop", `{"type":"message_stop"}`},
	}
	return join(start(200, 0), textBlock(0, "Let me check. "), server, result, resultStop, answer, end)
}

func chat(t *testing.T, api *FakeAPI, req llm.Request) llm.Reply {
	t.Helper()
	s, err := provider(api, nil).Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	r, err := llm.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func tools(t *testing.T, api *FakeAPI) []map[string]any {
	t.Helper()
	var out []map[string]any
	list, _ := api.Bodies[0]["tools"].([]any)
	for _, x := range list {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestCapabilitiesOfferWebSearch(t *testing.T) {
	if !claude.New(claude.Options{}).Capabilities().WebSearch {
		t.Error("the Claude provider should report web search")
	}
}

// With Request.WebSearch the request carries the current web search tool,
// called directly (no dynamic filtering), capped, and last (so it holds the
// cache breakpoint). Without it, there is no web tool.
func TestWebSearchToolInRequest(t *testing.T) {
	custom := []llm.ToolDef{{Name: "open_url", Description: "Open.", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}}

	api := NewFakeAPI(t, join(start(1, 0), textBlock(0, "Hi."), finish("end_turn", 1)))
	chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, Tools: custom, WebSearch: true})
	got := tools(t, api)
	if len(got) != 2 {
		t.Fatalf("tools = %v", got)
	}
	ws := got[1]
	if ws["type"] != "web_search_20260209" || ws["name"] != "web_search" || ws["max_uses"] != float64(3) {
		t.Errorf("web search tool = %v", ws)
	}
	if !reflect.DeepEqual(ws["allowed_callers"], []any{"direct"}) {
		t.Errorf("allowed_callers = %v, want [direct]", ws["allowed_callers"])
	}
	if ws["cache_control"] == nil || got[0]["cache_control"] != nil {
		t.Errorf("want the one cache breakpoint on the last tool, the web search: %v", got)
	}

	api = NewFakeAPI(t, join(start(1, 0), textBlock(0, "Hi."), finish("end_turn", 1)))
	chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, Tools: custom})
	for _, tl := range tools(t, api) {
		if tl["name"] == "web_search" {
			t.Errorf("web search offered without Request.WebSearch: %v", tl)
		}
	}
	if got := tools(t, api); len(got) != 1 || got[0]["cache_control"] == nil {
		t.Errorf("tools = %v, want the one custom tool with the breakpoint", got)
	}

	// With no other tools the search tool is the whole list.
	api = NewFakeAPI(t, join(start(1, 0), textBlock(0, "Hi."), finish("end_turn", 1)))
	chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, WebSearch: true})
	if got := tools(t, api); len(got) != 1 || got[0]["type"] != "web_search_20260209" {
		t.Errorf("tools = %v", got)
	}
}

// A reply that searched is read back whole: the text is joined, the usage
// counts the search, the results are marked untrusted, and the content is
// kept for replay.
func TestWebSearchReplyIsCollected(t *testing.T) {
	api := NewFakeAPI(t, searchReply("end_turn"))
	r := chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, WebSearch: true})
	if r.Message.Text != "Let me check. It is 62 and sunny." {
		t.Errorf("text = %q", r.Message.Text)
	}
	if len(r.Message.ToolCalls) != 0 || r.Stop != llm.StopEnd {
		t.Errorf("a server tool is no client tool call: calls %v stop %q", r.Message.ToolCalls, r.Stop)
	}
	if !r.Untrusted {
		t.Error("search results were not marked untrusted")
	}
	if r.Usage.WebSearches != 1 {
		t.Errorf("usage = %+v, want one web search", r.Usage)
	}
	if len(r.Message.Raw) == 0 {
		t.Fatal("no content kept for replay")
	}
}

// Server-tool blocks go back unchanged on the next call: the search, the
// encrypted results and the citation survive byte for byte.
func TestServerToolBlocksRoundTripInHistory(t *testing.T) {
	first := NewFakeAPI(t, searchReply("end_turn"))
	r := chat(t, first, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, WebSearch: true})

	api := NewFakeAPI(t, join(start(1, 0), textBlock(0, "Sure."), finish("end_turn", 1)))
	chat(t, api, llm.Request{WebSearch: true, Messages: []llm.Message{
		{Role: llm.User, Text: "weather"},
		r.Message,
		{Role: llm.User, Text: "and tomorrow?"},
	}})
	msgs := api.Bodies[0]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	asst := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("role = %v", asst["role"])
	}
	blocks := asst["content"].([]any)
	var types []string
	for _, b := range blocks {
		types = append(types, b.(map[string]any)["type"].(string))
	}
	if want := []string{"text", "server_tool_use", "web_search_tool_result", "text"}; !reflect.DeepEqual(types, want) {
		t.Fatalf("block types = %v, want %v", types, want)
	}
	use := blocks[1].(map[string]any)
	if use["id"] != "srvtoolu_1" || use["name"] != "web_search" || !reflect.DeepEqual(use["input"], map[string]any{"query": "raleigh weather today"}) {
		t.Errorf("server_tool_use = %v", use)
	}
	res := blocks[2].(map[string]any)
	if res["tool_use_id"] != "srvtoolu_1" {
		t.Errorf("result = %v", res)
	}
	var wantResult any
	_ = json.Unmarshal([]byte(searchResult), &wantResult)
	if got := res["content"].([]any)[0]; !reflect.DeepEqual(got, wantResult) {
		t.Errorf("search result changed:\n got %v\nwant %v", got, wantResult)
	}
	var wantCite any
	_ = json.Unmarshal([]byte(citation), &wantCite)
	answer := blocks[3].(map[string]any)
	if answer["text"] != "It is 62 and sunny." || !reflect.DeepEqual(answer["citations"].([]any)[0], wantCite) {
		t.Errorf("answer block = %v", answer)
	}
	if last := msgs[2].(map[string]any); last["role"] != "user" {
		t.Errorf("last = %v", last)
	}
}

// When the API pauses a long search turn, the stop reason says so and the
// paused content is kept, so the caller can send it back to go on.
func TestPausedSearchTurn(t *testing.T) {
	api := NewFakeAPI(t, searchReply("pause_turn"))
	r := chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "weather"}}, WebSearch: true})
	if r.Stop != llm.StopPause || len(r.Message.Raw) == 0 {
		t.Errorf("stop %q, raw %d bytes", r.Stop, len(r.Message.Raw))
	}
}

// An ordinary reply keeps no Raw, so ordinary history still goes through the
// plain text and tool-call path.
func TestPlainReplyHasNoRaw(t *testing.T) {
	api := NewFakeAPI(t, join(start(1, 0), textBlock(0, "Hi."), finish("end_turn", 1)))
	r := chat(t, api, llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}, WebSearch: true})
	if r.Message.Raw != nil || r.Untrusted || r.Usage.WebSearches != 0 {
		t.Errorf("reply = %+v", r)
	}
}
