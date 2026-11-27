package core_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/permissions"
	"github.com/irobinson010/footman/internal/plugins"
	tfake "github.com/irobinson010/footman/internal/plugins/fake"
	"github.com/irobinson010/footman/internal/registry"
)

// searchCaps is a provider that can search the web.
var searchCaps = llm.Caps{Tools: true, Caching: true, WebSearch: true}

// askRecorder answers every confirmation with answer and keeps the requests.
type askRecorder struct {
	mu     sync.Mutex
	answer bool
	reqs   []permissions.Request
}

func (a *askRecorder) Confirm(_ context.Context, r permissions.Request) (permissions.Decision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, r)
	return permissions.Decision{Allowed: a.answer, How: permissions.HowClick}, nil
}

func (a *askRecorder) requests() []permissions.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]permissions.Request(nil), a.reqs...)
}

func gateFor(t *testing.T, toml string, c permissions.Confirmer) *permissions.Gate {
	t.Helper()
	cfg, err := config.Parse([]byte(toml))
	if err != nil {
		t.Fatal(err)
	}
	return &permissions.Gate{Config: func() *config.Config { return cfg }, Confirmer: c}
}

// Web search is offered only when it is on and the provider can do it; the
// prompt says how to answer when it is, and that there is no web access when
// it is not.
func TestWebSearchOfferedOnlyWhenOnAndSupported(t *testing.T) {
	for _, tc := range []struct {
		name     string
		caps     llm.Caps
		on       bool
		offered  bool
		wantText string
	}{
		{"on and supported", searchCaps, true, true, "You can search the web."},
		{"turned off", searchCaps, false, false, "You have no web access"},
		{"provider cannot", llm.Caps{Tools: true}, true, false, "You have no web access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, func(_ *core.Deps, c *core.Config) { c.System = "PERSONA" })
			r.ai.SetCapabilities(tc.caps)
			r.p.SetWebSearch(tc.on)
			r.ai.Queue(lfake.Reply{Text: "Hello."})
			r.submit(core.TriggerWake, "Footman, what's the weather today?")
			req := r.ai.Requests()[0]
			if req.WebSearch != tc.offered {
				t.Errorf("Request.WebSearch = %v, want %v", req.WebSearch, tc.offered)
			}
			if !strings.Contains(req.System, tc.wantText) || !strings.HasPrefix(req.System, "PERSONA") {
				t.Errorf("system prompt = %q, want PERSONA then %q", req.System, tc.wantText)
			}
			if tc.offered && !strings.Contains(req.System, "ask which city once") {
				t.Errorf("the prompt does not tell the AI to ask for the city: %q", req.System)
			}
		})
	}
}

// The gate treats web search as a Read action: "deny" for reads switches it
// off, and WebSearchSpec says Read.
func TestWebSearchIsARead(t *testing.T) {
	if got := core.WebSearchSpec().Permission; got != plugins.Read {
		t.Fatalf("web search permission = %v, want read", got)
	}
	r := newRig(t, func(d *core.Deps, _ *core.Config) {
		d.Gate = gateFor(t, "[permissions]\nread = \"deny\"\n", nil)
	})
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(lfake.Reply{Text: "Hello."})
	r.submit(core.TriggerWake, "Footman, what's the weather?")
	req := r.ai.Requests()[0]
	if req.WebSearch || !strings.Contains(req.System, "You have no web access") {
		t.Errorf("web search offered although reads are denied: %v %q", req.WebSearch, req.System)
	}

	// The default policy lets it through without asking.
	conf := &askRecorder{}
	r = newRig(t, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	r.ai.Queue(lfake.Reply{Text: "Hello."})
	r.submit(core.TriggerWake, "Footman, what's the weather?")
	if !r.ai.Requests()[0].WebSearch || len(conf.requests()) != 0 {
		t.Errorf("default policy: offered %v, asked %d times", r.ai.Requests()[0].WebSearch, len(conf.requests()))
	}
}

// After a reply that used web search, search results are untrusted content
// (ADR-0015): an outward tool the model then calls needs a click even though
// the policy says auto, and is not run when the click is refused. Without a
// search the same call runs at once.
func TestSearchResultsAreUntrusted(t *testing.T) {
	run := func(t *testing.T, searched bool) (conf *askRecorder, mail *tfake.Tool, r *rig) {
		conf = &askRecorder{}
		mail = must(tfake.New(registry.Map{"name": "send_email", "description": "Send.", "permission": "outward", "results": []string{"sent"}}))
		r = newRig(t, func(d *core.Deps, _ *core.Config) {
			d.Tools = append(d.Tools, mail)
			d.Gate = gateFor(t, "[permissions]\noutward = \"auto\"\n", conf)
		})
		r.ai.SetCapabilities(searchCaps)
		r.p.SetWebSearch(true)
		r.ai.Queue(
			lfake.Reply{WebResults: searched, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "send_email", Args: json.RawMessage(`{"to":"x@example.com"}`)}}},
			lfake.Reply{Text: "Done."},
		)
		r.submit(core.TriggerWake, "Footman, look up the news and tell me")
		return conf, mail, r
	}

	t.Run("after a search", func(t *testing.T) {
		conf, mail, r := run(t, true)
		reqs := conf.requests()
		if len(reqs) != 1 || reqs[0].Level != plugins.Outward || !strings.Contains(reqs[0].Reason, "untrusted content") {
			t.Fatalf("confirmation requests = %+v, want one outward click about untrusted content", reqs)
		}
		if n := len(mail.Calls()); n != 0 {
			t.Errorf("send_email ran %d times after the click was refused", n)
		}
		res := r.ai.Requests()[1].Messages
		if last := res[len(res)-1]; last.Role != llm.ToolResult || !last.IsError || !strings.Contains(last.Text, "refused") {
			t.Errorf("tool result = %+v, want a refusal", last)
		}
	})
	t.Run("without a search", func(t *testing.T) {
		conf, mail, _ := run(t, false)
		if n := len(conf.requests()); n != 0 {
			t.Errorf("asked %d times without any search", n)
		}
		if n := len(mail.Calls()); n != 1 {
			t.Errorf("send_email ran %d times, want 1", n)
		}
	})
}

// A search is recorded for the action log as a read decided automatically.
func TestWebSearchIsLogged(t *testing.T) {
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", nil) })
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	tools := bus.Subscribe(r.bus, core.ToolCalled, 8)
	r.ai.Queue(lfake.Reply{Text: "62 and sunny.", WebResults: true})
	r.submit(core.TriggerWake, "Footman, what's the weather?")
	ev := await(t, tools, func(e core.ToolRan) bool { return e.Name == core.WebSearchTool })
	if ev.IsError || ev.Confirmation != "auto" || !strings.Contains(ev.Result, "1 web search") {
		t.Errorf("ToolRan = %+v", ev)
	}
}

// A paused search turn is sent back as it came, with its server-side
// blocks (Raw), and the next reply is the answer.
func TestPausedSearchContinues(t *testing.T) {
	r := newRig(t, nil)
	r.ai.SetCapabilities(searchCaps)
	r.p.SetWebSearch(true)
	raw := json.RawMessage(`[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}}]`)
	r.ai.Queue(
		lfake.Reply{Pause: true, Raw: raw},
		lfake.Reply{Text: "Sixty-two and sunny."},
	)
	r.submit(core.TriggerWake, "Footman, what's the weather?")
	reqs := r.ai.Requests()
	if len(reqs) != 2 {
		t.Fatalf("AI called %d times, want 2", len(reqs))
	}
	msgs := reqs[1].Messages
	if len(msgs) != 2 || msgs[1].Role != llm.Assistant || string(msgs[1].Raw) != string(raw) {
		t.Errorf("continuation messages = %+v, want the paused reply back with its Raw", msgs)
	}
	if !reqs[1].WebSearch {
		t.Error("the continuation dropped the web search tool")
	}
}

// With user.location set the system block says where the user is, on one
// line; without it, nothing about a location.
func TestLocationInSystemBlock(t *testing.T) {
	got := core.SystemBlockAt(testFacts, "Raleigh,\nNorth  Carolina")
	if !strings.Contains(got, "location") || !strings.Contains(got, "): Raleigh, North Carolina\n- Footman runs on") {
		t.Errorf("block lacks the location on one line:\n%s", got)
	}
	if strings.Contains(core.SystemBlock(testFacts), "location") || core.SystemBlockAt(testFacts, "  ") != core.SystemBlock(testFacts) {
		t.Error("a location line appears although none is set")
	}
	long := core.SystemBlockAt(testFacts, strings.Repeat("#", 500))
	if strings.Count(long, "#") != 100 {
		t.Errorf("location not cut to 100 characters: %d", strings.Count(long, "#"))
	}
}

// The location is in the request when set (through the system context the
// daemon builds), so "weather today" needs no question.
func TestLocationReachesTheAI(t *testing.T) {
	r := newRig(t, nil)
	r.p.SetSystemContext(core.SystemBlockAt(testFacts, "Raleigh"))
	r.ai.Queue(lfake.Reply{Text: "Hello."})
	r.submit(core.TriggerWake, "Footman, what's the weather today?")
	if sys := r.ai.Requests()[0].System; !strings.Contains(sys, "Raleigh") {
		t.Errorf("system prompt lacks the location: %q", sys)
	}
}
