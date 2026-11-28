package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
)

// "open those links" is not an app: it reaches the AI, which knows which
// links, while real apps and sites still open at once.
func TestOpenThoseLinksGoesToTheAI(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	r.ai.Queue(lfake.Reply{Text: "Which links?"})
	r.submit(core.TriggerWake, "Footman, open those links")
	if n := len(r.ai.Requests()); n != 1 {
		t.Errorf("%d AI requests, want 1", n)
	}
	for _, text := range []string{"open it", "open the first two links", "open the second one", "launch them"} {
		if _, ok := core.OpenRule(nil).Match(text); ok {
			t.Errorf("fast path took %q", text)
		}
	}
	for _, text := range []string{"open firefox", "open docs.example.org", "open visual studio code"} {
		if _, ok := core.OpenRule(nil).Match(text); !ok {
			t.Errorf("fast path missed %q", text)
		}
	}
}

// open_url takes several pages in one call, checks them all first, and
// stops at the limit.
func TestOpenURLSeveralPages(t *testing.T) {
	// run has the AI call open_url with args and returns the tool result it
	// was given back.
	run := func(t *testing.T, args string) (*rig, string) {
		r := newRig(t, nil)
		r.ai.Queue(
			lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_url", Args: json.RawMessage(args)}}},
			lfake.Reply{Text: "Done."},
		)
		r.submit(core.TriggerWake, "Footman, show me that stuff")
		msgs := r.ai.Requests()[1].Messages
		return r, msgs[len(msgs)-1].Text
	}
	t.Run("several", func(t *testing.T) {
		r, _ := run(t, `{"urls":["https://a.example","b.example/x"]}`)
		equal(t, "opened", r.launcher().URLs(), []string{"https://a.example", "https://b.example/x"})
	})
	t.Run("url and urls", func(t *testing.T) {
		r, _ := run(t, `{"url":"https://a.example","urls":["https://b.example"]}`)
		equal(t, "opened", r.launcher().URLs(), []string{"https://a.example", "https://b.example"})
	})
	t.Run("one bad address opens nothing", func(t *testing.T) {
		r, res := run(t, `{"urls":["https://a.example","not a url"]}`)
		if n := len(r.launcher().URLs()); n != 0 {
			t.Errorf("opened %d pages", n)
		}
		if !strings.Contains(res, "not a valid URL") {
			t.Errorf("result %q", res)
		}
	})
	t.Run("too many", func(t *testing.T) {
		r, res := run(t, `{"urls":["a.example","b.example","c.example","d.example","e.example","f.example"]}`)
		if n := len(r.launcher().URLs()); n != 0 {
			t.Errorf("opened %d pages", n)
		}
		if !strings.Contains(res, "at most 5") {
			t.Errorf("result %q", res)
		}
	})
	t.Run("none", func(t *testing.T) {
		r, res := run(t, `{}`)
		if n := len(r.launcher().URLs()); n != 0 || !strings.Contains(res, "required") {
			t.Errorf("opened %d, result %q", n, res)
		}
	})
}

// The break-glass stop forgets the conversation too.
func TestContextClearedByBreakGlass(t *testing.T) {
	r, _ := memRig(t, core.DefaultContextTurns, nil)
	r.ai.Queue(lfake.Reply{Text: "a"}, lfake.Reply{Text: "b"})
	r.submit(core.TriggerWake, "Footman, one")
	r.submit(core.TriggerWake, "Footman, stop everything")
	r.submit(core.TriggerWake, "Footman, two")
	equal(t, "after the stop", lines(r.ai.Requests()[1].Messages), []string{"user: two"})
}
