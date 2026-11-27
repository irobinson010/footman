package config

import "testing"

// Tightening is allowed by voice; loosening, and keys with no safe
// direction, need a click; secrets are never set by voice (ADR-0039).
func TestVoiceChangeTightenLoosen(t *testing.T) {
	list := func(s ...string) []string { return s }
	for _, c := range []struct {
		key      string
		old, new any
		want     Verdict
	}{
		// Ordinary keys are always allowed.
		{"replies.speak", "when-voice", "never", VoiceAllowed},
		// Permission levels: towards deny tightens.
		{"permissions.destructive", "confirm", "deny", VoiceAllowed},
		{"permissions.outward", "auto", "confirm", VoiceAllowed},
		{"permissions.read", "auto", "deny", VoiceAllowed},
		{"permissions.destructive", "deny", "confirm", VoiceNeedsClick},
		{"permissions.act", "confirm", "auto", VoiceNeedsClick},
		{"permissions.act", "auto", "bogus", VoiceNeedsClick},
		// No change is never a loosening.
		{"permissions.act", "auto", "auto", VoiceAllowed},
		// The action log: on tightens, off loosens.
		{"permissions.action_log", false, true, VoiceAllowed},
		{"permissions.action_log", true, false, VoiceNeedsClick},
		{"permissions.action_log_retention_days", int64(90), int64(30), VoiceNeedsClick},
		// Memory: off tightens.
		{"memory.enabled", true, false, VoiceAllowed},
		{"memory.enabled", false, true, VoiceNeedsClick},
		// ai.mode: towards offline tightens.
		{"ai.mode", "cloud", "local-first", VoiceAllowed},
		{"ai.mode", "local-first", "offline", VoiceAllowed},
		{"ai.mode", "cloud", "offline", VoiceAllowed},
		{"ai.mode", "offline", "cloud", VoiceNeedsClick},
		{"ai.mode", "offline", "local-first", VoiceNeedsClick},
		// ai.web_search: off tightens by voice, on needs a click.
		{"ai.web_search", true, false, VoiceAllowed},
		{"ai.web_search", false, true, VoiceNeedsClick},
		{"ai.web_search", true, true, VoiceAllowed},
		// user.location is plain text the user may say.
		{"user.location", "", "Raleigh", VoiceAllowed},
		// Context: removing items tightens, adding loosens.
		{"context.cloud", list("clipboard", "selection"), list("selection"), VoiceAllowed},
		{"context.cloud", list("clipboard"), list(), VoiceAllowed},
		{"context.cloud", list("all"), list("clipboard", "window_title"), VoiceAllowed},
		{"context.cloud", list(), list("clipboard"), VoiceNeedsClick},
		{"context.cloud", list("clipboard"), list("selection"), VoiceNeedsClick},
		{"context.local", list("clipboard"), list("all"), VoiceNeedsClick},
		{"context.local", list("window_title", "clipboard", "selection"), list("all"), VoiceNeedsClick}, // "all" also covers items added later
		{"context.cloud", list("system"), list(), VoiceAllowed},                                         // "stop telling the AI about my computer"
		{"context.cloud", list(), list("system"), VoiceNeedsClick},
		{"context.cloud", list("all"), list("window_title", "clipboard", "selection"), VoiceAllowed}, // all minus system
		{"context.local", list("system", "clipboard"), list("system"), VoiceAllowed},
		// No safe direction: always a click.
		{"web.listen", "127.0.0.1:7878", "127.0.0.1:7879", VoiceNeedsClick},
		{"ai.providers.x.url", "", "http://x", VoiceNeedsClick},
		{"ai.budget.x.monthly_usd", 10.0, 5.0, VoiceNeedsClick},
		// Secrets: never.
		{"ai.providers.x.api_key", nil, "keychain:x", VoiceNever},
		// Unknown.
		{"no.such.key", nil, "x", VoiceUnknown},
	} {
		if _, got, why := VoiceChange(c.key, c.old, c.new); got != c.want {
			t.Errorf("%s %v → %v = %v (%s), want %v", c.key, c.old, c.new, got, why, c.want)
		} else if got == VoiceNeedsClick && why == "" {
			t.Errorf("%s: no reason given", c.key)
		}
	}
}

func TestVoicePolicyDirectionless(t *testing.T) {
	if _, v, _ := VoicePolicy("permissions.act"); v != VoiceNeedsClick {
		t.Errorf("permissions.act = %v", v)
	}
	if _, v, _ := VoicePolicy("ai.providers.x.api_key"); v != VoiceNever {
		t.Errorf("api_key = %v", v)
	}
}

func TestContextAllows(t *testing.T) {
	for _, c := range []struct {
		list []string
		item string
		want bool
	}{
		{nil, "system", false},
		{[]string{"system"}, "system", true},
		{[]string{"clipboard"}, "system", false},
		{[]string{"all"}, "system", true},
		{[]string{"all"}, "clipboard", true},
	} {
		if got := ContextAllows(c.list, c.item); got != c.want {
			t.Errorf("ContextAllows(%v, %q) = %v, want %v", c.list, c.item, got, c.want)
		}
	}
	if d := Defaults().Strings("context.cloud"); !ContextAllows(d, "system") {
		t.Errorf("context.cloud defaults to %v, want system on (ADR-0043)", d)
	}
}

func TestParseValueEnumUnderscores(t *testing.T) {
	v, err := ParseValue("context.cloud", "window title and clipboard")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.([]string); !ok || len(got) != 2 || got[0] != "window_title" || got[1] != "clipboard" {
		t.Errorf("got %#v", v)
	}
}
