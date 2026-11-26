package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/plugins"
	"github.com/irobinson010/footman/internal/speech"
)

// klaus is a persona with one line of each kind.
func klaus() core.Persona {
	lines := map[core.LineKind]string{
		core.LineDone: "It is done, Herr Ian.", core.LineError: "That did not work, Herr Ian.", core.LineConfirm: "Shall I proceed, Herr Ian?",
	}
	return core.Persona{
		ID: "klaus", Name: "Klaus", System: "be Klaus",
		Voice: speech.Voice{ID: "kokoro-en", Speaker: "bm_lewis"},
		Line:  func(k core.LineKind) (string, bool) { l, ok := lines[k]; return l, ok },
	}
}

func shown(r *rig) []string {
	var out []string
	for _, n := range r.plat.Notifier().(*pfake.Notifier).Shown() {
		out = append(out, n.Title+": "+n.Body)
	}
	return out
}

// A persona set while running gives the voice, the system prompt and the
// notification title; replies are spoken and shown (replies.notify).
func TestSetPersonaVoicePromptAndNotify(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.Notify = true })
	r.p.SetPersona(klaus())
	r.ai.Queue(lfake.Reply{Text: "Jawohl."})
	r.submit(core.TriggerWake, "footman, hello")
	sp := r.tts.Spoken()
	if len(sp) != 1 || sp[0].Text != "Jawohl." || sp[0].Voice.Speaker != "bm_lewis" {
		t.Errorf("spoken = %+v", sp)
	}
	if reqs := r.ai.Requests(); len(reqs) != 1 || reqs[0].System != "be Klaus" {
		t.Errorf("system prompt = %+v", reqs)
	}
	equal(t, "shown", shown(r), []string{"Klaus: Jawohl."})
}

// With replies.notify off a spoken reply is not shown; quiet mode still
// shows it, since it is not spoken.
func TestReplyNotifySetting(t *testing.T) {
	r := newRig(t, nil)
	r.ai.Queue(lfake.Reply{Text: "One."}, lfake.Reply{Text: "Two."}, lfake.Reply{Text: "Three."})
	r.submit(core.TriggerWake, "footman, a")
	if s := shown(r); len(s) != 0 {
		t.Errorf("shown with notify off: %v", s)
	}
	r.p.SetQuiet(true)
	r.submit(core.TriggerWake, "footman, b")
	r.p.SetQuiet(false)
	r.p.SetReplyNotify(true)
	r.submit(core.TriggerWake, "footman, c")
	equal(t, "shown", shown(r), []string{"Footman: Two.", "Footman: Three."})
	equal(t, "spoken", spoken(r.tts), []string{"One.", "Three."})
}

// A command that works with nothing to say gets the done line; one that
// fails gets the error line, spoken, with the error in the notification.
func TestPersonaDoneAndErrorLines(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) {
		c.Notify = true
		c.FastRules = []core.FastRule{{Name: "quiet", Permission: plugins.Act, Match: func(text string) (core.FastAction, bool) {
			switch text {
			case "nothing":
				return func(context.Context, *core.Pipeline) (string, error) { return "", nil }, true
			case "break":
				return func(context.Context, *core.Pipeline) (string, error) { return "", errors.New("boom") }, true
			}
			return nil, false
		}}}
	})
	r.p.SetPersona(klaus())
	r.submit(core.TriggerWake, "footman, nothing")
	if err := r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "footman, break"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Submit = %v, want the failure", err)
	}
	equal(t, "spoken", spoken(r.tts), []string{"It is done, Herr Ian.", "That did not work, Herr Ian."})
	s := shown(r)
	if len(s) != 2 || !strings.Contains(s[1], "That did not work") || !strings.Contains(s[1], "boom") {
		t.Errorf("shown = %q", s)
	}
}

// Without persona lines a failure is silent, as before.
func TestNoPersonaLinesKeepsFailureSilent(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) {
		c.FastRules = []core.FastRule{{Name: "x", Permission: plugins.Act, Match: func(string) (core.FastAction, bool) {
			return func(context.Context, *core.Pipeline) (string, error) { return "", errors.New("boom") }, true
		}}}
	})
	_ = r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "footman, x"})
	if s := spoken(r.tts); len(s) != 0 {
		t.Errorf("spoken %v", s)
	}
}

// askGate asks for a click on every call and gets it: core.Confirming is
// what the daemon's confirmer calls as it asks.
type askGate struct{}

func (askGate) Check(ctx context.Context, _ plugins.ToolSpec, _ json.RawMessage, _ bool) error {
	core.Confirming(ctx)
	return nil
}

// The confirm line is spoken while a confirmation waits.
func TestConfirmLineWhileWaiting(t *testing.T) {
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.Gate = askGate{} })
	r.p.SetPersona(klaus())
	r.ai.Queue(lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "1", Name: "weather", Args: []byte(`{}`)}}}, lfake.Reply{Text: "Sunny."})
	r.submit(core.TriggerWake, "footman, weather")
	equal(t, "spoken", spoken(r.tts), []string{"Shall I proceed, Herr Ian?", "Sunny."})
}

type fakeSwitcher map[string]string

func (f fakeSwitcher) Find(name string) (string, bool) { id, ok := f[name]; return id, ok }
func (f fakeSwitcher) Switch(_ context.Context, id string) (string, error) {
	return "switched to " + id, nil
}

func TestPersonaRule(t *testing.T) {
	rule := core.PersonaRule(fakeSwitcher{"klaus": "klaus", "lucy": "lucy"})
	for text, want := range map[string]string{
		"switch to Klaus":                "switched to klaus",
		"Use Lucy.":                      "switched to lucy",
		"please change persona to klaus": "switched to klaus",
		"let me talk to Lucy":            "switched to lucy",
		"switch to the Klaus persona":    "switched to klaus",
		"use google":                     "",
		"switch to dark mode":            "",
		"open klaus":                     "",
		"use":                            "",
	} {
		run, ok := rule.Match(text)
		if (want != "") != ok {
			t.Errorf("%q matched = %v", text, ok)
			continue
		}
		if ok {
			if got, _ := run(context.Background(), nil); got != want {
				t.Errorf("%q = %q, want %q", text, got, want)
			}
		}
	}
}

func TestRouterSetWakeWords(t *testing.T) {
	r := core.NewRouter([]string{"footman"}, nil, nil)
	if got := r.Route(core.TriggerWake, "Klaus, open YouTube").Text; got != "Klaus, open YouTube" {
		t.Errorf("before: %q", got)
	}
	r.SetWakeWords([]string{"klaus", "hey klaus", "footman"})
	if got := r.Route(core.TriggerWake, "Klaus, open YouTube").Text; got != "open YouTube" {
		t.Errorf("after: %q", got)
	}
}
