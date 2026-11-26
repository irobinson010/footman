package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
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

func settingsRig(t *testing.T) (*core.Pipeline, *rig, *fileStore, *bus.Subscription[core.ConfirmRequest]) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(settingsFile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st := &fileStore{path, cfg}
	var s *core.Settings
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		s = &core.Settings{Store: st, Bus: d.Bus}
		c.FastRules = append([]core.FastRule{core.SettingsRule(s)}, core.DefaultFastRules(nil)...)
		d.Tools = append(d.Tools, core.SettingsTools(s)...)
	})
	return r.p, r, st, bus.Subscribe(r.bus, core.SettingsConfirm, 8)
}

func TestQuietModeByVoice(t *testing.T) {
	_, r, st, _ := settingsRig(t)
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
	_, r, st, conf := settingsRig(t)
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
	select {
	case c := <-conf.C():
		t.Errorf("unexpected confirmation %+v", c)
	default:
	}
}

func TestSecuritySettingRefusedWithClick(t *testing.T) {
	_, r, st, conf := settingsRig(t)
	rep := bus.Subscribe(r.bus, core.Replied, 8)
	before, _ := os.ReadFile(st.path)
	r.submit(core.TriggerWake, "set permissions destructive to auto")
	ev := await(t, rep, func(core.Reply) bool { return true })
	if !strings.Contains(ev.Text, "won't change") || !strings.Contains(ev.Text, "click") {
		t.Errorf("reply %q", ev.Text)
	}
	req := await(t, conf, func(core.ConfirmRequest) bool { return true })
	if req.Key != "permissions.destructive" || req.Value != "auto" || req.ID == "" || req.Reason == "" {
		t.Errorf("request %+v", req)
	}
	after, _ := os.ReadFile(st.path)
	if string(before) != string(after) {
		t.Error("config file changed")
	}
	// Also refused through the AI tools, and for a secret.
	s := &core.Settings{Store: st, Bus: r.bus}
	for _, key := range []string{"ai.mode", "memory.enabled", "web.listen", "ai.providers.x.api_key", "permissions.act"} {
		if _, status := s.Set(key, "x"); status != core.Refused {
			t.Errorf("%s: status %v, want refused", key, status)
		}
	}
}

func TestSettingsTools(t *testing.T) {
	_, _, st, conf := settingsRig(t)
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
	_ = conf
}
