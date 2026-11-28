package core_test

import (
	"strings"
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
		// A bare "dictate" or "write" starts a dictation session, a command
		// the fast path handles (ADR-0045); more words after it are still a
		// one-shot dictation.
		{core.TriggerWake, "dictate", core.Route{Kind: core.RouteCommand, Text: "dictate"}},
		{core.TriggerWake, "Alfred, write.", core.Route{Kind: core.RouteCommand, Text: "write."}},
		{core.TriggerWake, "Alfred take a note", core.Route{Kind: core.RouteCommand, Text: "take a note"}},
		{core.TriggerWake, "Alfred Wright.", core.Route{Kind: core.RouteCommand, Text: "Wright."}},
		{core.TriggerWake, "Alfred, Wright in a new note.", core.Route{Kind: core.RouteCommand, Text: "Wright in a new note."}},
		{core.TriggerWake, "Alfred write in a new note", core.Route{Kind: core.RouteCommand, Text: "write in a new note"}},
		{core.TriggerWake, "Alfred open meeting notes and write", core.Route{Kind: core.RouteCommand, Text: "open meeting notes and write"}},
		{core.TriggerWake, "Alfred write a new note to Sam", core.Route{Kind: core.RouteDictation, Text: "a new note to Sam"}},
		{core.TriggerWake, "Alfred, open YouTube", core.Route{Kind: core.RouteCommand, Text: "open YouTube"}},
		{core.TriggerWake, "fred, open YouTube", core.Route{Kind: core.RouteCommand, Text: "open YouTube"}},
		{core.TriggerWake, "Lfred open YouTube", core.Route{Kind: core.RouteCommand, Text: "open YouTube"}},
		{core.TriggerWake, "red light therapy", core.Route{Kind: core.RouteCommand, Text: "red light therapy"}},
		{core.TriggerWake, "alfredo recipe", core.Route{Kind: core.RouteCommand, Text: "alfredo recipe"}},
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

func TestMatchWake(t *testing.T) {
	r := core.NewRouter([]string{"ashford", "alfred", "hey footman", "footman"}, nil, nil)
	cases := []struct {
		text, rest string
		ok         bool
	}{
		{"Ashford, open YouTube", "open YouTube", true},
		{"Ashferd open YouTube.", "open YouTube.", true},
		{"Alfred.", "", true},
		{"Alfred, what's the time?", "what's the time?", true},
		{"Alford, what's the time?", "what's the time?", true},
		{"Esfort Open YouTube.", "Open YouTube.", true},
		{"Hey, Footman. Open YouTube", "Open YouTube", true},
		{"Hey Foot man, open YouTube", "open YouTube", true},
		{"Hayfootman. Good morning", "Good morning", true},
		{"A footman. Good morning", "Good morning", true},
		{"Hey fuman. Good morning", "Good morning", true},
		{"Footman, open YouTube", "open YouTube", true},
		{"ford, open YouTube", "open YouTube", true},
		{"— Alfred, open YouTube", "open YouTube", true},
		{"", "", false},
		{"Open YouTube", "", false},
		{"The officers were about to take leave", "", false},
		{"I'll ask Fred tomorrow", "", false},
		{"and then the Alfred Hitchcock film", "", false},
		{"Red light", "", false},
		{"Alpha, what time is it?", "", false},
		{"Effort open YouTube", "", false},
		{"Sometimes I think", "", false},
	}
	for _, c := range cases {
		rest, ok := r.MatchWake(c.text)
		if ok != c.ok || rest != c.rest {
			t.Errorf("MatchWake(%q) = %q, %v; want %q, %v", c.text, rest, ok, c.rest, c.ok)
		}
	}
}

// TestMatchWakeRenderings checks the spellings speech-to-text gives the
// shipped and common extra wake words, and the near misses that must not
// count. The spotter has already fired before this check, so it only has to
// tell a wake word from other speech.
func TestMatchWakeRenderings(t *testing.T) {
	r := core.NewRouter([]string{"ashford", "hey ashford", "alfred", "klaus", "lucy", "hey footman", "footman"}, nil, nil)
	for _, text := range []string{
		// Alfred
		"Alfred, open YouTube", "Al Fred, open YouTube", "Al-fred open YouTube", "All Fred open YouTube",
		"Alfredo open YouTube", "Alfred's open YouTube", "Alfie, open YouTube", "Alfreed open YouTube",
		"Elfred open YouTube", "Alford open YouTube", "Fred, open YouTube", "Hey Alfred, open YouTube",
		"Hey, Alfred open YouTube", "Okay Alfred open YouTube", "Hello Alfred, open YouTube",
		// Klaus
		"Klaus, open YouTube", "Klaus open YouTube", "Claus, open YouTube", "Claws open YouTube",
		"Close open YouTube", "Klaas open YouTube", "Hey Klaus, open YouTube",
		// Ashford
		"Ashford, open YouTube", "Ash ford, open YouTube", "Ash Ford open YouTube", "Ashfort open YouTube",
		"Ashferd open YouTube", "Ashford's open YouTube", "Hey Ashford, open YouTube", "Hey Ash ford open YouTube",
		"Ash for open YouTube", "ford, open YouTube",
	} {
		if rest, ok := r.MatchWake(text); !ok || !strings.Contains(rest, "open YouTube") {
			t.Errorf("MatchWake(%q) = %q, %v; want a match leaving the command", text, rest, ok)
		}
	}
	for _, text := range []string{
		"Open YouTube", "Alpha, open YouTube", "Albany open YouTube", "Alfa Romeo is a car",
		"Ashley, open YouTube", "Ashworth open YouTube", "Ash, open YouTube", "Asphalt open YouTube",
		"Cloud open YouTube", "Clean up open YouTube",
		"Lucas open YouTube", "I think Alfred said so", "She told Klaus to open YouTube", "The Alfred Hitchcock film",
		"Hey you, open YouTube", "Hi there, open YouTube",
	} {
		if rest, ok := r.MatchWake(text); ok {
			t.Errorf("MatchWake(%q) = %q, true; want no match", text, rest)
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
