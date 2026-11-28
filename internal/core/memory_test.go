package core_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
)

// memClock is a clock the test moves.
type memClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *memClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *memClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newMemClock() *memClock {
	return &memClock{t: time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)}
}

// memRig is a rig whose pipeline remembers turns exchanges.
func memRig(t *testing.T, turns int, edit func(*core.Deps, *core.Config)) (*rig, *memClock) {
	t.Helper()
	clock := newMemClock()
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		c.ContextTurns, c.Now = turns, clock.Now
		if edit != nil {
			edit(d, c)
		}
	})
	return r, clock
}

// lines renders a request's messages as "role: text" for comparison.
func lines(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, string(m.Role)+": "+m.Text)
	}
	return out
}

const (
	vacRaw  = `[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"best vacuums"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://vacs.example/a","title":"Best vacuums of 2026","encrypted_content":"BIGBLOB=="}]},{"type":"text","text":"The top picks are the Dyson V20 and the Shark Rocket."}]`
	vacText = "The top picks are the Dyson V20 and the Shark Rocket."
)

var vacSources = []llm.Source{
	{Title: "Best vacuums of 2026", URL: "https://vacs.example/a"},
	{Title: "Shark   Rocket\nreview", URL: "https://shark.example/rocket"},
	{Title: "", URL: "https://dyson.example/v20"},
}

// searchReply is an answer that used the web, with its raw blocks and pages.
func searchReply() lfake.Reply {
	return lfake.Reply{Text: vacText, Raw: json.RawMessage(vacRaw), Sources: vacSources, WebResults: true}
}

// A second wake command that refers to the first sees the first exchange,
// with its search results and pages, and the open_url it asks for needs one
// confirmation for all the pages.
func TestContextCarriesAcrossWakeCommands(t *testing.T) {
	conf := &askRecorder{answer: true}
	r, _ := memRig(t, core.DefaultContextTurns, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(searchReply())
	r.submit(core.TriggerWake, "Footman, what are the best rated vacuums?")

	args := `{"urls":["https://vacs.example/a","https://shark.example/rocket"]}`
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(args)}}},
		lfake.Reply{Text: "Opened them."},
	)
	r.submit(core.TriggerWake, "Footman, open the first two links")

	reqs := r.ai.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3", len(reqs))
	}
	second := reqs[1].Messages
	if len(second) != 3 || second[0].Text != "what are the best rated vacuums?" || second[1].Role != llm.Assistant || second[2].Text != "open the first two links" {
		t.Fatalf("second request = %v, want the first exchange then the new request", lines(second))
	}
	if string(second[1].Raw) != vacRaw {
		t.Errorf("the latest exchange lost its raw search blocks: %s", second[1].Raw)
	}
	if !strings.Contains(reqs[1].System, "open_url once with urls") {
		t.Error("the system prompt does not tell the model it can open links from earlier answers")
	}

	// The web content tainted the carried history, so opening needs one
	// confirmation, for both pages.
	asked := conf.requests()
	if len(asked) != 1 || !strings.Contains(asked[0].Reason, "untrusted content") {
		t.Fatalf("confirmations = %+v, want one about untrusted content", asked)
	}
	if asked[0].Question != "Shall I open vacs.example and shark.example?" {
		t.Errorf("question = %q", asked[0].Question)
	}
	if !strings.Contains(asked[0].Detail, "https://shark.example/rocket") {
		t.Errorf("the confirmation does not show the pages: %q", asked[0].Detail)
	}
	equal(t, "opened", r.launcher().URLs(), []string{"https://vacs.example/a", "https://shark.example/rocket"})
}

// A refused confirmation opens nothing, even though the model asked for
// pages from an earlier answer.
func TestContextTaintStillGatesWhenRefused(t *testing.T) {
	conf := &askRecorder{answer: false}
	r, _ := memRig(t, core.DefaultContextTurns, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(searchReply())
	r.submit(core.TriggerWake, "Footman, best vacuums?")
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(`{"url":"https://vacs.example/a"}`)}}},
		lfake.Reply{Text: "Okay."},
	)
	r.submit(core.TriggerWake, "Footman, open that")
	if n := len(r.launcher().URLs()); n != 0 {
		t.Errorf("opened %d pages after a refusal", n)
	}
}

// Without untrusted content in the history, open_url needs no confirmation.
func TestContextWithoutWebContentIsTrusted(t *testing.T) {
	conf := &askRecorder{answer: true}
	r, _ := memRig(t, core.DefaultContextTurns, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
	r.ai.Queue(lfake.Reply{Text: "Sure."})
	r.submit(core.TriggerWake, "Footman, hello")
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(`{"url":"https://example.com"}`)}}},
		lfake.Reply{Text: "Done."},
	)
	r.submit(core.TriggerWake, "Footman, take me to example dot com")
	if n := len(conf.requests()); n != 0 {
		t.Errorf("asked %d times with nothing untrusted in the conversation", n)
	}
	equal(t, "opened", r.launcher().URLs(), []string{"https://example.com"})
}

// Older exchanges lose their raw search blocks and tool traffic but keep the
// words of the answer and a note of the pages, with titles and addresses.
func TestContextTrimsOldRawBlocksButKeepsSources(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(searchReply())
	r.submit(core.TriggerWake, "Footman, best vacuums?")
	r.ai.Queue(lfake.Reply{Text: "Glad to help."})
	r.submit(core.TriggerWake, "Footman, thanks")
	r.ai.Queue(lfake.Reply{Text: "Anything else."})
	r.submit(core.TriggerWake, "Footman, that's it")

	reqs := r.ai.Requests()
	third := reqs[2].Messages
	if len(third) != 5 {
		t.Fatalf("third request has %d messages: %v", len(third), lines(third))
	}
	for i, m := range third {
		if len(m.Raw) > 0 || len(m.ToolCalls) > 0 || m.Role == llm.ToolResult {
			t.Errorf("message %d keeps provider blocks: %+v", i, m)
		}
	}
	note := third[1].Text
	for _, want := range []string{
		vacText,
		"Sources from that answer:",
		"1. Best vacuums of 2026 (https://vacs.example/a)",
		"2. Shark Rocket review (https://shark.example/rocket)",
		"3. https://dyson.example/v20",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	if strings.Contains(note, "BIGBLOB") {
		t.Error("the encrypted search result was kept")
	}
	// Still compacted on the request after.
	r.ai.Queue(lfake.Reply{Text: "Bye."})
	r.submit(core.TriggerWake, "Footman, bye")
	if got := r.ai.Requests()[3].Messages[1].Text; got != note {
		t.Errorf("the compacted exchange changed between requests:\n%q\n%q", got, note)
	}
}

// The tool calls and results of an older exchange go too.
func TestContextDropsOldToolTraffic(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "weather", Args: json.RawMessage(`{}`)}}},
		lfake.Reply{Text: "It is sunny."},
		lfake.Reply{Text: "Yes."},
		lfake.Reply{Text: "Fine."},
	)
	r.submit(core.TriggerWake, "Footman, weather?")
	r.submit(core.TriggerWake, "Footman, really?")
	r.submit(core.TriggerWake, "Footman, ok")
	reqs := r.ai.Requests()
	// Right after it the exchange is whole: the request, the tool call and
	// its result, the answer.
	equal(t, "second request", lines(reqs[2].Messages), []string{"user: weather?", "assistant: ", "tool: fake tool: no result queued", "assistant: It is sunny.", "user: really?"})
	if len(reqs[2].Messages[1].ToolCalls) != 1 {
		t.Error("the latest exchange lost its tool call")
	}
	// Once another exchange follows, it is only the words.
	equal(t, "later request", lines(reqs[3].Messages), []string{
		"user: weather?", "assistant: It is sunny.",
		"user: really?", "assistant: Yes.",
		"user: ok",
	})
}

// The memory holds the last few exchanges, and the oldest go first.
func TestContextTurnLimit(t *testing.T) {
	r, _ := memRig(t, 2, nil)
	for _, q := range []string{"one", "two", "three", "four"} {
		r.ai.Queue(lfake.Reply{Text: "re " + q})
		r.submit(core.TriggerWake, "Footman, "+q)
	}
	r.ai.Queue(lfake.Reply{Text: "re five"})
	r.submit(core.TriggerWake, "Footman, five")
	reqs := r.ai.Requests()
	equal(t, "request 5", lines(reqs[4].Messages), []string{
		"user: three", "assistant: re three",
		"user: four", "assistant: re four",
		"user: five",
	})
}

// With the memory off, every command starts afresh.
func TestContextOff(t *testing.T) {
	r, _ := memRig(t, 0, nil)
	r.ai.Queue(lfake.Reply{Text: "a"}, lfake.Reply{Text: "b"})
	r.submit(core.TriggerWake, "Footman, one")
	r.submit(core.TriggerWake, "Footman, two")
	equal(t, "second request", lines(r.ai.Requests()[1].Messages), []string{"user: two"})
	if strings.Contains(r.ai.Requests()[1].System, "earlier answers") {
		t.Error("the system prompt mentions earlier answers while the memory is off")
	}
}

// The memory is forgotten after the ttl since the last exchange, and each
// exchange pushes the deadline back.
func TestContextExpires(t *testing.T) {
	r, clock := memRig(t, core.DefaultContextTurns, func(_ *core.Deps, c *core.Config) { c.ContextTTL = 10 * time.Minute })
	r.ai.Queue(lfake.Reply{Text: "a"}, lfake.Reply{Text: "b"}, lfake.Reply{Text: "c"}, lfake.Reply{Text: "d"})
	r.submit(core.TriggerWake, "Footman, one")
	clock.Advance(9 * time.Minute)
	r.submit(core.TriggerWake, "Footman, two")
	if n := len(r.ai.Requests()[1].Messages); n != 3 {
		t.Errorf("after 9 minutes the request has %d messages, want 3", n)
	}
	clock.Advance(9 * time.Minute) // 18 since the first, 9 since the last
	r.submit(core.TriggerWake, "Footman, three")
	if n := len(r.ai.Requests()[2].Messages); n != 5 {
		t.Errorf("9 minutes after the last exchange the request has %d messages, want 5", n)
	}
	clock.Advance(11 * time.Minute)
	r.submit(core.TriggerWake, "Footman, four")
	equal(t, "after expiry", lines(r.ai.Requests()[3].Messages), []string{"user: four"})
}

// An expired memory takes its taint with it.
func TestContextTaintExpires(t *testing.T) {
	conf := &askRecorder{answer: true}
	r, clock := memRig(t, core.DefaultContextTurns, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(searchReply())
	r.submit(core.TriggerWake, "Footman, best vacuums?")
	clock.Advance(11 * time.Minute)
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(`{"url":"https://example.com"}`)}}},
		lfake.Reply{Text: "Done."},
	)
	r.submit(core.TriggerWake, "Footman, take me to example dot com")
	if n := len(conf.requests()); n != 0 {
		t.Errorf("asked %d times after the tainted exchange expired", n)
	}
}

// Taint belongs to the exchange that brought the content in: once that
// exchange is out of the window, Act tools no longer need confirmation.
func TestContextTaintLeavesWithItsExchange(t *testing.T) {
	conf := &askRecorder{answer: true}
	r, _ := memRig(t, 2, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(searchReply(), lfake.Reply{Text: "b"}, lfake.Reply{Text: "c"})
	r.submit(core.TriggerWake, "Footman, best vacuums?")
	r.submit(core.TriggerWake, "Footman, thanks")
	r.submit(core.TriggerWake, "Footman, and again") // pushes the search out
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(`{"url":"https://example.com"}`)}}},
		lfake.Reply{Text: "Done."},
	)
	r.submit(core.TriggerWake, "Footman, take me to example dot com")
	if n := len(conf.requests()); n != 0 {
		t.Errorf("asked %d times after the tainted exchange left the memory", n)
	}
}

// "Forget that", "start over" and "new topic" clear the memory without
// calling the AI, and the next request starts afresh and untainted.
func TestContextForgetPhrases(t *testing.T) {
	for _, phrase := range []string{"forget that", "start over", "new topic", "Start over, please."} {
		t.Run(phrase, func(t *testing.T) {
			conf := &askRecorder{answer: true}
			r, _ := memRig(t, core.DefaultContextTurns, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
			r.ai.SetCapabilities(searchCaps)
			r.p.SetWebSearch(true)
			r.ai.Queue(searchReply())
			r.submit(core.TriggerWake, "Footman, best vacuums?")
			r.submit(core.TriggerWake, "Footman, "+phrase)
			if n := len(r.ai.Requests()); n != 1 {
				t.Fatalf("%d AI requests, want 1: forgetting is not an AI request", n)
			}
			equal(t, "spoken", spoken(r.tts), []string{vacText, "Okay, starting fresh."})
			r.ai.Queue(
				lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(`{"url":"https://example.com"}`)}}},
				lfake.Reply{Text: "Done."},
			)
			r.submit(core.TriggerWake, "Footman, take me to example dot com")
			equal(t, "request", lines(r.ai.Requests()[1].Messages), []string{"user: take me to example dot com"})
			if n := len(conf.requests()); n != 0 {
				t.Errorf("asked %d times after forgetting", n)
			}
		})
	}
}

// Only the whole phrase forgets: it must not catch other requests.
func TestForgetRuleMatchesWholePhrasesOnly(t *testing.T) {
	rule := core.ForgetRule()
	for _, text := range []string{"forget that", "Forget that.", "please start over", "new topic", "start fresh"} {
		if _, ok := rule.Match(text); !ok {
			t.Errorf("%q did not match", text)
		}
	}
	for _, text := range []string{"forget that my name is Bob", "start over the song", "what is a new topic", "forget", "open the topic"} {
		if _, ok := rule.Match(text); ok {
			t.Errorf("%q matched", text)
		}
	}
}

// A different persona has not said what the conversation holds, so the
// memory is cleared; setting the same persona again keeps it.
func TestContextClearedOnPersonaSwitch(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	alfred := core.Persona{System: "You are Alfred."}
	r.p.SetPersona(alfred)
	r.ai.Queue(lfake.Reply{Text: "a"}, lfake.Reply{Text: "b"}, lfake.Reply{Text: "c"})
	r.submit(core.TriggerWake, "Footman, one")
	r.p.SetPersona(alfred) // as when the config reloads
	r.submit(core.TriggerWake, "Footman, two")
	if n := len(r.ai.Requests()[1].Messages); n != 3 {
		t.Errorf("the same persona set again cleared the memory: %d messages", n)
	}
	r.p.SetPersona(core.Persona{System: "You are Klaus."})
	r.submit(core.TriggerWake, "Footman, three")
	equal(t, "after the switch", lines(r.ai.Requests()[2].Messages), []string{"user: three"})
}

// Changing the setting applies at once: fewer turns drop the oldest, zero
// forgets everything.
func TestSetContextLive(t *testing.T) {
	r, _ := memRig(t, 4, nil)
	r.ai.Queue(lfake.Reply{Text: "a"}, lfake.Reply{Text: "b"}, lfake.Reply{Text: "c"}, lfake.Reply{Text: "d"})
	r.submit(core.TriggerWake, "Footman, one")
	r.submit(core.TriggerWake, "Footman, two")
	r.p.SetContext(1, 0)
	r.submit(core.TriggerWake, "Footman, three")
	equal(t, "one turn", lines(r.ai.Requests()[2].Messages), []string{"user: two", "assistant: b", "user: three"})
	r.p.SetContext(0, 0)
	r.submit(core.TriggerWake, "Footman, four")
	equal(t, "off", lines(r.ai.Requests()[3].Messages), []string{"user: four"})
}

// A reply that is a question still takes its answer in the same
// conversation, now from the memory.
func TestContextCarriesFollowUpQuestions(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	r.ai.Queue(lfake.Reply{Text: "Which city?"}, lfake.Reply{Text: "Sunny."})
	r.submit(core.TriggerWake, "Footman, weather?")
	r.submit(core.TriggerWake, "Footman, Raleigh")
	equal(t, "second", lines(r.ai.Requests()[1].Messages), []string{"user: weather?", "assistant: Which city?", "user: Raleigh"})
}

// A failed request is not remembered.
func TestContextSkipsFailedRequests(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	r.ai.Queue(lfake.Reply{Err: errors.New("quota exceeded")}, lfake.Reply{Text: "fine"})
	_ = r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "Footman, one"})
	r.submit(core.TriggerWake, "Footman, two")
	equal(t, "second", lines(r.ai.Requests()[1].Messages), []string{"user: two"})
}
