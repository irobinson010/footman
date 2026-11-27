package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/plugins"
)

// After a web search, an Act tool such as open_url needs a click even though
// permissions.act is auto (ADR-0047, which extends ADR-0015); without a search
// it runs at once. A Read tool stays automatic either way.
func TestSearchResultsTightenActToo(t *testing.T) {
	run := func(t *testing.T, searched bool, tool string, args string) (*askRecorder, *rig) {
		conf := &askRecorder{answer: true}
		r := newRig(t, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
		r.ai.SetCapabilities(searchCaps)
		r.p.SetWebSearch(true)
		r.ai.Queue(
			lfake.Reply{WebResults: searched, ToolCalls: []llm.ToolCall{{ID: "t1", Name: tool, Args: json.RawMessage(args)}}},
			lfake.Reply{Text: "Done."},
		)
		r.submit(core.TriggerWake, "Footman, find a good site and open it")
		return conf, r
	}

	t.Run("open_url after a search", func(t *testing.T) {
		conf, r := run(t, true, "open_url", `{"url":"https://example.com"}`)
		reqs := conf.requests()
		if len(reqs) != 1 || reqs[0].Level != plugins.Act || !strings.Contains(reqs[0].Reason, "untrusted content") {
			t.Fatalf("confirmation requests = %+v, want one act confirmation about untrusted content", reqs)
		}
		if reqs[0].ClickOnly || reqs[0].Question != "Shall I open example.com?" {
			t.Errorf("request = %+v, want a voice-answerable question", reqs[0])
		}
		equal(t, "opened", r.launcher().URLs(), []string{"https://example.com"}) // allowed
	})
	t.Run("open_url refused after a search", func(t *testing.T) {
		conf := &askRecorder{answer: false}
		r := newRig(t, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
		r.ai.SetCapabilities(searchCaps)
		r.p.SetWebSearch(true)
		r.ai.Queue(
			lfake.Reply{WebResults: true, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(`{"url":"https://example.com"}`)}}},
			lfake.Reply{Text: "Done."},
		)
		r.submit(core.TriggerWake, "Footman, find a good site and open it")
		if n := len(r.launcher().URLs()); n != 0 {
			t.Errorf("opened %d pages after the click was refused", n)
		}
	})
	t.Run("open_url without a search", func(t *testing.T) {
		conf, r := run(t, false, "open_url", `{"url":"https://example.com"}`)
		if n := len(conf.requests()); n != 0 {
			t.Errorf("asked %d times without any search", n)
		}
		equal(t, "opened", r.launcher().URLs(), []string{"https://example.com"})
	})
	t.Run("open_app and quick_action after a search", func(t *testing.T) {
		for tool, args := range map[string]string{"open_app": `{"app":"firefox"}`, "quick_action": `{"action":"mute"}`} {
			conf, _ := run(t, true, tool, args)
			if n := len(conf.requests()); n != 1 {
				t.Errorf("%s: asked %d times after a search, want 1", tool, n)
			}
		}
	})
	t.Run("a Read tool stays automatic", func(t *testing.T) {
		conf, _ := run(t, true, "weather", `{}`)
		if n := len(conf.requests()); n != 0 {
			t.Errorf("a read tool was confirmed %d times after a search", n)
		}
	})
}

// With nobody to ask (the default gate), Act is refused once untrusted
// content is in the task and still runs otherwise.
func TestDefaultGateRefusesActWhenUntrusted(t *testing.T) {
	g := core.DefaultGate{}
	spec := plugins.ToolSpec{Name: "open_url", Permission: plugins.Act}
	if _, err := g.Decide(t.Context(), spec, nil, false); err != nil {
		t.Errorf("act without untrusted content: %v", err)
	}
	if _, err := g.Decide(t.Context(), spec, nil, true); err == nil {
		t.Error("act with untrusted content was allowed")
	}
	read := plugins.ToolSpec{Name: "weather", Permission: plugins.Read}
	if _, err := g.Decide(t.Context(), read, nil, true); err != nil {
		t.Errorf("read with untrusted content: %v", err)
	}
}
