package core_test

import (
	"testing"

	"github.com/irobinson010/footman/internal/core"
)

func TestRouter(t *testing.T) {
	r := core.NewRouter([]string{"Alfred"}, nil, nil)
	cases := []struct {
		trig core.Trigger
		text string
		want core.Route
	}{
		{core.TriggerWake, "Alfred, dictate hello world", core.Route{Kind: core.RouteDictation, Text: "hello world"}},
		{core.TriggerWake, "Write: dear Sam", core.Route{Kind: core.RouteDictation, Text: "dear Sam"}},
		{core.TriggerWake, "dictate", core.Route{Kind: core.RouteNone}},
		{core.TriggerWake, "Alfred, open YouTube", core.Route{Kind: core.RouteCommand, Text: "open YouTube"}},
		{core.TriggerWake, "what's the time", core.Route{Kind: core.RouteCommand, Text: "what's the time"}},
		{core.TriggerWake, "Alfred.", core.Route{Kind: core.RouteNone}},
		{core.TriggerWake, "", core.Route{Kind: core.RouteNone}},
		{core.TriggerWake, "writer's block is real", core.Route{Kind: core.RouteCommand, Text: "writer's block is real"}},
		{core.TriggerDictate, "open YouTube", core.Route{Kind: core.RouteDictation, Text: "open YouTube"}},
		{core.TriggerDictate, "dictate this", core.Route{Kind: core.RouteDictation, Text: "dictate this"}},
		{core.TriggerCommand, "write a haiku", core.Route{Kind: core.RouteCommand, Text: "write a haiku"}},
	}
	for _, c := range cases {
		if got := r.Route(c.trig, c.text); got != c.want {
			t.Errorf("Route(%v, %q) = %+v, want %+v", c.trig, c.text, got, c.want)
		}
	}
}

func TestBreakGlassPhrase(t *testing.T) {
	r := core.NewRouter([]string{"Alfred"}, nil, nil)
	cases := []struct {
		trig core.Trigger
		text string
		want bool
	}{
		{core.TriggerWake, "Alfred, stop everything", true},
		{core.TriggerWake, "STOP EVERYTHING!", true},
		{core.TriggerWake, "cancel everything.", true},
		{core.TriggerWake, "stop everything and make tea", false},
		{core.TriggerWake, "stop", false},
		{core.TriggerDictate, "stop everything", false},
		{core.TriggerCommand, "stop everything", false},
	}
	for _, c := range cases {
		if got := r.BreakGlass(c.trig, c.text); got != c.want {
			t.Errorf("BreakGlass(%v, %q) = %v, want %v", c.trig, c.text, got, c.want)
		}
	}
}

func TestOpenRuleMatching(t *testing.T) {
	rule := core.OpenRule(core.DefaultSites)
	for _, text := range []string{"open", "open .", "opening hours", "please open YouTube", "what is open source"} {
		if _, ok := rule.Match(text); ok {
			t.Errorf("open rule matched %q", text)
		}
	}
}

func TestNames(t *testing.T) {
	if core.Idle.String() != "idle" || core.FastPath.String() != "fast_path" || core.State(99).String() != "State(99)" {
		t.Error("State.String")
	}
	if core.TriggerWake.String() != "wake" || core.Trigger(0).String() != "Trigger(0)" {
		t.Error("Trigger.String")
	}
	if core.RouteCommand.String() != "command" || core.RouteKind(9).String() != "RouteKind(9)" {
		t.Error("RouteKind.String")
	}
}
