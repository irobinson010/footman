package core_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/plugins"
)

// fileStore edits a real config file, like api.ConfigFile.
type fileStore struct {
	path string
	cur  *config.Config
}

func (f *fileStore) Current() *config.Config { return f.cur }
func (f *fileStore) Set(k string, v any) (*config.Config, error) {
	if err := config.SetFile(f.path, k, v); err != nil {
		return nil, err
	}
	c, err := config.Load(f.path)
	f.cur = c
	return c, err
}

const settingsFile = "# my notes\nversion = 1\n\n[replies]\n# keep this comment\nspeak = \"when-voice\"\n"

// fakeConfirmer records click-confirmation requests; a test answers them.
type fakeConfirmer struct {
	mu    sync.Mutex
	asked []core.ConfirmRequest
	done  []func(bool, string)
}

func (f *fakeConfirmer) AskSetting(r core.ConfirmRequest, done func(bool, string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r)
	f.done = append(f.done, done)
	return strconv.Itoa(len(f.asked)), nil
}

func (f *fakeConfirmer) requests() []core.ConfirmRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]core.ConfirmRequest(nil), f.asked...)
}

// answer answers the i-th request (0-based), as the broker would.
func (f *fakeConfirmer) answer(t *testing.T, i int, allowed bool, how string) {
	t.Helper()
	f.mu.Lock()
	if i >= len(f.done) {
		f.mu.Unlock()
		t.Fatalf("no request %d", i)
	}
	done := f.done[i]
	f.mu.Unlock()
	done(allowed, how)
}

type settingsRigT struct {
	*rig
	st   *fileStore
	conf *fakeConfirmer
	s    *core.Settings
	told chan string
}

func settingsRig(t *testing.T) *settingsRigT {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(settingsFile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	sr := &settingsRigT{st: &fileStore{path, cfg}, conf: &fakeConfirmer{}, told: make(chan string, 8)}
	sr.rig = newRig(t, func(d *core.Deps, c *core.Config) {
		sr.s = &core.Settings{Store: sr.st, Bus: d.Bus, Confirm: sr.conf, Told: func(s string) { sr.told <- s }}
		c.FastRules = append([]core.FastRule{core.SettingsRule(sr.s)}, core.DefaultFastRules(nil)...)
		d.Tools = append(d.Tools, core.SettingsTools(sr.s)...)
	})
	return sr
}

func TestQuietModeByVoice(t *testing.T) {
	sr := settingsRig(t)
	r, st := sr.rig, sr.st
	rep := bus.Subscribe(r.bus, core.Replied, 8)
	r.submit(core.TriggerWake, "turn on quiet mode")
	if got := st.cur.String("replies.speak"); got != "never" {
		t.Fatalf("replies.speak = %q, want never", got)
	}
	if ev := await(t, rep, func(core.Reply) bool { return true }); !strings.Contains(ev.Text, "Quiet mode is on") {
		t.Errorf("reply %q", ev.Text)
	}
	r.submit(core.TriggerWake, "Turn quiet mode off.")
	if got := st.cur.String("replies.speak"); got != "when-voice" {
		t.Fatalf("replies.speak = %q, want when-voice", got)
	}
	if len(r.ai.Requests()) != 0 {
		t.Error("went to the AI")
	}
	// comments survive the edits
	b, _ := os.ReadFile(st.path)
	if !strings.Contains(string(b), "# my notes") || !strings.Contains(string(b), "# keep this comment") {
		t.Errorf("comments lost:\n%s", b)
	}
}

// "turn off web search" applies at once (a tightening); "turn on web search"
// needs a click; "set my location to Raleigh" applies at once.
func TestWebSearchAndLocationByVoice(t *testing.T) {
	sr := settingsRig(t)
	r, st := sr.rig, sr.st
	r.submit(core.TriggerWake, "turn off web search")
	if st.cur.Bool("ai.web_search") {
		t.Fatal("ai.web_search is still on after turning it off by voice")
	}
	r.submit(core.TriggerWake, "turn on web search")
	if st.cur.Bool("ai.web_search") {
		t.Fatal("turning web search back on by voice needed no click")
	}
	if len(sr.conf.requests()) != 1 {
		t.Errorf("click requests = %d, want 1", len(sr.conf.requests()))
	}
	r.submit(core.TriggerWake, "set my location to Raleigh")
	if got := st.cur.String("user.location"); got != "Raleigh" {
		t.Errorf("user.location = %q, want Raleigh", got)
	}
	if len(r.ai.Requests()) != 0 {
		t.Error("went to the AI")
	}
}

func TestSettingsRulePhrases(t *testing.T) {
	s := &core.Settings{Store: &fileStore{cur: config.Defaults()}}
	rule := core.SettingsRule(s)
	for _, text := range []string{"turn off the lights", "what is the weather", "set the table", "open youtube", "set a timer to 5 minutes", "turn on the wake"} {
		if _, ok := rule.Match(text); ok {
			t.Errorf("matched %q", text)
		}
	}
	for _, text := range []string{"what is my dictate key", "turn off the wake word", "set history retention days to 7", "quiet mode on"} {
		if _, ok := rule.Match(text); !ok {
			t.Errorf("did not match %q", text)
		}
	}
}

func TestWhatIsMyDictateKey(t *testing.T) {
	s := &core.Settings{Store: &fileStore{cur: config.Defaults()}}
	run, ok := core.SettingsRule(s).Match("what is my dictate key")
	if !ok {
		t.Fatal("no match")
	}
	got, err := run(t.Context(), nil)
	if err != nil || got != "Input dictate key is F8." {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestInvalidValueRejected(t *testing.T) {
	sr := settingsRig(t)
	r, st := sr.rig, sr.st
	rep := bus.Subscribe(r.bus, core.Replied, 8)
	r.submit(core.TriggerWake, "set replies speak to loudly")
	ev := await(t, rep, func(core.Reply) bool { return true })
	if !strings.Contains(ev.Text, "always, when-voice, never") {
		t.Errorf("unhelpful reply %q", ev.Text)
	}
	if st.cur.String("replies.speak") != "when-voice" {
		t.Error("value changed")
	}
	r.submit(core.TriggerWake, "set history retention days to lots")
	// An invalid loosening is refused before anything is asked.
	r.submit(core.TriggerWake, "set permissions destructive to whenever")
	if got := sr.conf.requests(); len(got) != 0 {
		t.Errorf("unexpected confirmations %+v", got)
	}
}

// Loosening by voice asks for a click and changes nothing until Allow; the
// change is then applied as asked and logged as a click.
func TestLooseningNeedsClick(t *testing.T) {
	sr := settingsRig(t)
	r, st := sr.rig, sr.st
	rep := bus.Subscribe(r.bus, core.Replied, 8)
	changed := bus.Subscribe(r.bus, core.SettingChanged, 8)
	before, _ := os.ReadFile(st.path)
	r.submit(core.TriggerWake, "set permissions destructive to auto")
	ev := await(t, rep, func(core.Reply) bool { return true })
	if !strings.Contains(ev.Text, "won't change") || !strings.Contains(ev.Text, "footman confirm allow 1") {
		t.Errorf("reply %q", ev.Text)
	}
	reqs := sr.conf.requests()
	if len(reqs) != 1 || reqs[0].Key != "permissions.destructive" || reqs[0].Old != "confirm" || reqs[0].New != "auto" || reqs[0].Reason == "" {
		t.Fatalf("requests %+v", reqs)
	}
	after, _ := os.ReadFile(st.path)
	if string(before) != string(after) {
		t.Error("config file changed before the click")
	}

	sr.conf.answer(t, 0, true, "click")
	if got := st.cur.String("permissions.destructive"); got != "auto" {
		t.Errorf("after Allow permissions.destructive = %q", got)
	}
	c := await(t, changed, func(core.SettingChange) bool { return true })
	if c.Key != "permissions.destructive" || c.Old != "confirm" || c.New != "auto" || c.How != "click" || c.Actor != core.ActorOwner {
		t.Errorf("setting change %+v", c)
	}
	if told := <-sr.told; !strings.Contains(told, "changed from confirm to auto") {
		t.Errorf("told %q", told)
	}
}

func TestLooseningDeniedOrExpired(t *testing.T) {
	sr := settingsRig(t)
	for i, how := range []string{"click", "timeout", "cancelled"} {
		if _, st := sr.s.Set("ai.mode", "offline"); st != core.Changed {
			t.Fatal("tightening ai.mode refused")
		}
		if _, st := sr.s.Set("ai.mode", "cloud"); st != core.Refused {
			t.Fatalf("%s: loosening ai.mode not refused", how)
		}
		sr.conf.answer(t, i, false, how)
		if got := sr.st.cur.String("ai.mode"); got != "offline" {
			t.Errorf("%s: ai.mode = %q after a refusal", how, got)
		}
		told := <-sr.told
		if want := map[bool]string{true: "in time", false: "not allowed"}[how == "timeout"]; !strings.Contains(told, want) {
			t.Errorf("%s: told %q", how, told)
		}
	}
}

// Tightening takes effect at once by voice; the reverse needs a click.
// Secrets are never set by voice.
func TestTightenByVoiceLoosenByClick(t *testing.T) {
	sr := settingsRig(t)
	s, st := sr.s, sr.st
	for _, c := range []struct {
		key, from, to string
		want          core.Status
	}{
		{"permissions.destructive", "confirm", "deny", core.Changed},
		{"permissions.destructive", "deny", "confirm", core.Refused},
		{"permissions.act", "auto", "confirm", core.Changed},
		{"permissions.act", "confirm", "auto", core.Refused},
		{"permissions.action_log", "off", "on", core.Changed},
		{"permissions.action_log", "on", "off", core.Refused},
		{"memory.enabled", "on", "off", core.Changed},
		{"memory.enabled", "off", "on", core.Refused},
		{"ai.mode", "cloud", "local first", core.Changed},
		{"ai.mode", "local-first", "cloud", core.Refused},
		{"context.cloud", "clipboard, selection", "clipboard", core.Changed},
		{"context.cloud", "clipboard", "clipboard, selection", core.Refused},
		{"context.local", "all", "window title", core.Changed},
		{"context.local", "window title", "all", core.Refused},
		{"web.listen", "127.0.0.1:7878", "0.0.0.0:7878", core.Refused},
	} {
		// Put the old value in place by hand, as the owner would.
		v, err := config.ParseValue(c.key, c.from)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Set(c.key, v); err != nil {
			t.Fatal(err)
		}
		n := len(sr.conf.requests())
		reply, status := s.Set(c.key, c.to)
		if status != c.want {
			t.Errorf("%s %s → %s: status %v, want %v (%s)", c.key, c.from, c.to, status, c.want, reply)
		}
		asked := len(sr.conf.requests()) > n
		if asked != (c.want == core.Refused) {
			t.Errorf("%s %s → %s: asked for a click = %v", c.key, c.from, c.to, asked)
		}
		if c.want == core.Refused && core.ValueText(st.cur.Get(c.key)) != core.ValueText(v) {
			t.Errorf("%s changed without a click", c.key)
		}
	}
	n := len(sr.conf.requests())
	if reply, status := s.Set("ai.providers.claude.api_key", "keychain:x"); status != core.Refused || !strings.Contains(reply, "secret") {
		t.Errorf("secret: %v %q", status, reply)
	}
	if len(sr.conf.requests()) != n {
		t.Error("a secret was offered for confirmation")
	}
}

// "set destructive actions to deny" applies at once; "turn off the action
// log" waits for a click.
func TestDoneWhenPhrases(t *testing.T) {
	sr := settingsRig(t)
	r, st := sr.rig, sr.st
	rep := bus.Subscribe(r.bus, core.Replied, 8)
	r.submit(core.TriggerWake, "set destructive actions to deny")
	if got := st.cur.String("permissions.destructive"); got != "deny" {
		t.Errorf("permissions.destructive = %q", got)
	}
	await(t, rep, func(core.Reply) bool { return true })
	r.submit(core.TriggerWake, "turn off the action log")
	if ev := await(t, rep, func(core.Reply) bool { return true }); !strings.Contains(ev.Text, "won't change") {
		t.Errorf("reply %q", ev.Text)
	}
	if !st.cur.Bool("permissions.action_log") {
		t.Fatal("action log turned off by voice")
	}
	// A spoken "yes" is just another utterance: it answers nothing, and the
	// AI trying the change again only asks again.
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "set_setting", Args: json.RawMessage(`{"key":"permissions.action_log","value":"off"}`)}}},
		lfake.Reply{Text: "Done."},
	)
	r.submit(core.TriggerWake, "yes, allow it")
	if !st.cur.Bool("permissions.action_log") {
		t.Fatal("action log turned off without a click")
	}
	if n := len(sr.conf.requests()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
	sr.conf.answer(t, 0, true, "click")
	if st.cur.Bool("permissions.action_log") {
		t.Error("action log still on after Allow")
	}
}

func TestNoConfirmer(t *testing.T) {
	sr := settingsRig(t)
	s := &core.Settings{Store: sr.st}
	if reply, status := s.Set("permissions.act", "deny"); status != core.Changed {
		t.Errorf("tightening without a confirmer: %v (%s)", status, reply)
	}
	if reply, status := s.Set("permissions.act", "auto"); status != core.Refused || !strings.Contains(reply, "can't ask") {
		t.Errorf("loosening without a confirmer: %v (%s)", status, reply)
	}
}

func TestSettingsTools(t *testing.T) {
	sr := settingsRig(t)
	st := sr.st
	s := &core.Settings{Store: st}
	tools := map[string]plugins.Tool{}
	for _, tl := range core.SettingsTools(s) {
		tools[tl.Spec().Name] = tl
	}
	run := func(name, args string) plugins.Result {
		res, err := tools[name].Run(t.Context(), []byte(args))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run("set_setting", `{"key":"replies.speak","value":"never"}`); res.IsError || st.cur.String("replies.speak") != "never" {
		t.Errorf("set_setting: %+v", res)
	}
	if res := run("get_setting", `{"key":"replies.speak"}`); !strings.Contains(res.Text, "never") {
		t.Errorf("get_setting: %+v", res)
	}
	if res := run("list_settings", `{}`); !strings.Contains(res.Text, "replies.speak = never") || strings.Contains(res.Text, "permissions.") {
		t.Errorf("list_settings: %s", res.Text)
	}
	if res := run("set_setting", `{"key":"replies.speak","value":"bogus"}`); !res.IsError {
		t.Error("bogus value accepted")
	}
	// The AI may tighten but not loosen.
	if res := run("set_setting", `{"key":"permissions.outward","value":"deny"}`); res.IsError || st.cur.String("permissions.outward") != "deny" {
		t.Errorf("tighten: %+v", res)
	}
	if res := run("set_setting", `{"key":"permissions.outward","value":"auto"}`); !res.IsError || st.cur.String("permissions.outward") != "deny" {
		t.Errorf("loosen: %+v", res)
	}
}

// The dictation stop phrases can be set by voice (ADR-0045).
func TestDictationStopPhrasesByVoice(t *testing.T) {
	sr := settingsRig(t)
	sr.rig.submit(core.TriggerWake, "set dictation stop phrases to finish dictation, we are done")
	got := sr.st.cur.Strings("dictation.stop_phrases")
	if len(got) != 2 || got[0] != "finish dictation" || got[1] != "we are done" {
		t.Errorf("stop phrases = %v, want [finish dictation, we are done]", got)
	}
}
