package core_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/plugins"
)

func dictCall(args string) lfake.Reply {
	return lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "d1", Name: core.StartDictationTool, Args: json.RawMessage(args)}}}
}

func offeredTool(r *rig, name string) bool {
	for _, req := range r.ai.Requests() {
		for _, t := range req.Tools {
			if t.Name == name {
				return true
			}
		}
	}
	return false
}

// A misheard request goes to the AI, which starts a session in the focused
// app through the tool.
func TestStartDictationToolFocused(t *testing.T) {
	r := dictRig(t, nil)
	r.ai.Queue(dictCall(`{}`), lfake.Reply{Text: "Go ahead."})
	r.session("Alfred, let's do some dictation", "meeting at nine", "stop dictation")
	equal(t, "typed", r.typed(), []string{"meeting at nine"})
	if !offeredTool(r.rig, core.StartDictationTool) {
		t.Error("start_dictation was not offered")
	}
	if p := r.ai.Requests()[0].System; !strings.Contains(p, "stop dictation") || !strings.Contains(p, "start_dictation") {
		t.Errorf("system prompt lacks the dictation guidance: %q", p)
	}
}

func TestStartDictationToolNewNote(t *testing.T) {
	r := dictRig(t, nil)
	r.focusOnOpen()
	r.ai.Queue(dictCall(`{"target":"new_note"}`), lfake.Reply{Text: "Go ahead."})
	r.session("Alfred, I want to jot something down", "milk and eggs", "stop dictation")
	path := filepath.Join(r.notes, "note 2026-11-27 14.30.12.txt")
	if got := r.launcher().Opened(); len(got) != 1 || got[0].Target != path {
		t.Errorf("opened %+v, want %s", got, path)
	}
	equal(t, "typed", r.typed(), []string{"milk and eggs"})
}

func TestStartDictationToolFile(t *testing.T) {
	finder := &fakeFinder{found: []string{"/home/me/Documents/budget.txt"}}
	r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Finder = finder })
	r.focusOnOpen()
	r.ai.Queue(dictCall(`{"target":"file","file":"budget"}`), lfake.Reply{Text: "Go ahead."})
	r.session("Alfred, I want to write in my budget", "rent is due", "stop dictation")
	equal(t, "asked", finder.asked, []string{"budget"})
	equal(t, "typed", r.typed(), []string{"rent is due"})
}

// Without the always-on microphone the tool is not offered, and the prompt
// does not mention it.
func TestStartDictationToolNeedsWake(t *testing.T) {
	r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Wake = nil })
	r.ai.Queue(lfake.Reply{Text: "Hello."})
	r.submit(core.TriggerWake, "Alfred, let's do some dictation")
	if offeredTool(r.rig, core.StartDictationTool) {
		t.Error("start_dictation offered with no wake engine")
	}
	if p := r.ai.Requests()[0].System; strings.Contains(p, "start_dictation") {
		t.Errorf("prompt mentions start_dictation: %q", p)
	}
}

// A bad call is an error result, and nothing starts.
func TestStartDictationToolBadArgs(t *testing.T) {
	for _, args := range []string{`{"target":"elsewhere"}`, `{"target":"file"}`} {
		r := dictRig(t, nil)
		r.ai.Queue(dictCall(args), lfake.Reply{Text: "Sorry."})
		r.submit(core.TriggerWake, "Alfred, let's do some dictation")
		if r.p.Dictation().Active {
			t.Errorf("%s: started a session", args)
		}
	}
}

// After untrusted content the tool needs a confirmation like any Act tool
// (ADR-0047); a refusal starts nothing.
func TestStartDictationToolAfterSearchConfirms(t *testing.T) {
	for _, answer := range []bool{true, false} {
		conf := &askRecorder{answer: answer}
		r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Gate = gateFor(t, "", conf) })
		r.ai.SetCapabilities(searchCaps)
		r.p.SetWebSearch(true)
		r.ai.Queue(
			lfake.Reply{WebResults: true, ToolCalls: dictCall(`{}`).ToolCalls},
			lfake.Reply{Text: "Go ahead."},
		)
		r.submit(core.TriggerWake, "Alfred, look something up and let's write")
		reqs := conf.requests()
		if len(reqs) != 1 || reqs[0].Level != plugins.Act || reqs[0].Question != "Shall I start dictation?" {
			t.Fatalf("requests = %+v, want one act confirmation", reqs)
		}
		if r.p.Dictation().Active != answer {
			t.Errorf("answer %v: active = %v", answer, r.p.Dictation().Active)
		}
	}
}
