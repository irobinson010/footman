package permissions_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/permissions"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/plugins"
	tfake "github.com/irobinson010/footman/internal/plugins/fake"
	"github.com/irobinson010/footman/internal/registry"
	sfake "github.com/irobinson010/footman/internal/speech/fake"
	"github.com/irobinson010/footman/internal/store"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// rig is a pipeline with the real gate and action log over a real store.
type rig struct {
	t     *testing.T
	bus   *bus.Bus
	st    *store.Store
	ai    *lfake.Provider
	tool  *tfake.Tool
	p     *core.Pipeline
	gate  *permissions.Gate
	cfg   *config.Config
	mu    sync.Mutex
	stopL func()
}

func newRig(t *testing.T, cfgSrc string, c permissions.Confirmer) *rig {
	t.Helper()
	r := &rig{t: t, bus: bus.New(), cfg: mustParse(t, "version = 1\n"+cfgSrc)}
	r.st = must(store.Open(context.Background(), filepath.Join(t.TempDir(), "footman.db")))
	r.ai = must(lfake.New(registry.Map(nil)))
	r.tool = must(tfake.New(registry.Map{"name": "delete_file", "permission": "destructive", "results": []string{"deleted"}}))
	r.gate = &permissions.Gate{Config: r.config, Confirmer: c}
	r.p = must(core.New(core.Deps{
		Bus: r.bus, Platform: must(pfake.New(registry.Map{"root": t.TempDir()})), STT: must(sfake.NewSTT(registry.Map(nil))),
		LLM: r.ai, Tools: []plugins.Tool{r.tool}, Gate: r.gate,
	}, core.Config{}))
	r.stopL = (&permissions.Log{Store: r.st, Config: r.config, Logf: t.Logf}).Watch(r.bus)
	t.Cleanup(func() {
		r.p.Close()
		r.stopL()
		r.bus.Close()
		_ = r.st.Close()
	})
	return r
}

func (r *rig) config() *config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *rig) setConfig(src string) {
	c := mustParse(r.t, "version = 1\n"+src)
	r.mu.Lock()
	r.cfg = c
	r.mu.Unlock()
}

func (r *rig) submit(text string) {
	r.t.Helper()
	if err := r.p.Submit(context.Background(), core.Utterance{Trigger: core.TriggerCommand, Text: text}); err != nil {
		r.t.Fatal(err)
	}
}

// entries stops the log (so every event received is written) and returns
// the log, oldest first.
func (r *rig) entries() []store.ActionEntry {
	r.t.Helper()
	r.stopL()
	es, err := r.st.RecentActions(context.Background(), 100)
	if err != nil {
		r.t.Fatal(err)
	}
	for i, j := 0, len(es)-1; i < j; i, j = i+1, j-1 {
		es[i], es[j] = es[j], es[i]
	}
	return es
}

const deleteArgs = `{"path":"/home/me/old.txt","token":"hunter2"}`

func (r *rig) queueDelete() {
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "delete_file", Args: json.RawMessage(deleteArgs)}}},
		lfake.Reply{Text: "Done."},
	)
}

// The "done when" of #82: a Destructive tool with destructive = confirm runs
// only after the confirmer allows it, and the call lands in the log with its
// secrets redacted.
func TestDestructiveToolRunsOnlyAfterConfirmation(t *testing.T) {
	for _, allow := range []bool{true, false} {
		name := map[bool]string{true: "allowed", false: "refused"}[allow]
		t.Run(name, func(t *testing.T) {
			fc := &fakeConfirmer{d: permissions.Decision{Allowed: allow, How: permissions.HowClick}}
			r := newRig(t, "[permissions]\ndestructive = \"confirm\"\n", fc)
			r.queueDelete()
			r.submit("delete my old notes")

			if got := len(r.tool.Calls()); got != map[bool]int{true: 1, false: 0}[allow] {
				t.Errorf("tool ran %d times", got)
			}
			if reqs := fc.asked(); len(reqs) != 1 || !strings.Contains(reqs[0].Detail, "/home/me/old.txt") {
				t.Errorf("confirmer asked %+v", reqs)
			}
			es := r.entries()
			if len(es) != 1 {
				t.Fatalf("log = %+v, want one entry", es)
			}
			e := es[0]
			if e.Action != permissions.ActionTool || e.Tool != "delete_file" || e.Actor != core.ActorAPI ||
				e.Confirmation != permissions.HowClick || e.Reason != "delete my old notes" {
				t.Errorf("entry = %+v", e)
			}
			if strings.Contains(e.Args, "hunter2") || !strings.Contains(e.Args, "/home/me/old.txt") {
				t.Errorf("args = %q, want the path kept and the token redacted", e.Args)
			}
			if allow && e.Result != "deleted" {
				t.Errorf("result = %q", e.Result)
			}
			if !allow && !strings.HasPrefix(e.Result, "error: refused") {
				t.Errorf("result = %q, want a refusal", e.Result)
			}
		})
	}
}

// Without a confirmer, the default policy refuses destructive tools and the
// AI is told why.
func TestDestructiveToolRefusedWithoutConfirmer(t *testing.T) {
	r := newRig(t, "", nil)
	r.queueDelete()
	r.submit("delete my old notes")
	if len(r.tool.Calls()) != 0 {
		t.Error("destructive tool ran with nobody to confirm it")
	}
	msgs := r.ai.Requests()[1].Messages
	if res := msgs[len(msgs)-1]; !res.IsError || !strings.Contains(res.Text, "confirmation") {
		t.Errorf("tool result = %+v, want a refusal that says why", res)
	}
	if es := r.entries(); len(es) != 1 || es[0].Confirmation != permissions.HowPolicy {
		t.Errorf("log = %+v", es)
	}
}

func TestFastPathLogged(t *testing.T) {
	r := newRig(t, "", nil)
	r.submit("open youtube")
	es := r.entries()
	if len(es) != 1 {
		t.Fatalf("log = %+v", es)
	}
	e := es[0]
	if e.Action != permissions.ActionFastPath || e.Tool != "open" || e.Confirmation != permissions.HowAuto ||
		e.Result != "Opening youtube." || e.Reason != "open youtube" || e.Actor != core.ActorAPI {
		t.Errorf("entry = %+v", e)
	}
}

// act = deny stops fast-path actions too.
func TestFastPathDenied(t *testing.T) {
	r := newRig(t, "[permissions]\nact = \"deny\"\n", nil)
	r.submit("open youtube")
	es := r.entries()
	if len(es) != 1 || es[0].Confirmation != permissions.HowPolicy || !strings.HasPrefix(es[0].Result, "error: refused") {
		t.Fatalf("log = %+v", es)
	}
}

func TestSettingChangesLogged(t *testing.T) {
	r := newRig(t, "", nil)
	old := r.config()
	r.setConfig("[dictation]\ninsert = \"paste\"\n[ai.providers.claude]\napi_key = \"keychain:claude\"\n")
	permissions.PublishConfigChanges(r.bus, old, r.config(), core.ActorAPI, permissions.HowAuto, "config.set")
	bus.Publish(r.bus, core.SettingChanged, core.SettingChange{
		Key: "replies.speak", Old: "when-voice", New: "never", Actor: core.ActorOwner, How: permissions.HowVoice, Reason: "changed by voice",
	})
	es := r.entries()
	if len(es) != 3 {
		t.Fatalf("log = %+v", es)
	}
	byKey := map[string]store.ActionEntry{}
	for _, e := range es {
		if e.Action != permissions.ActionSetting {
			t.Errorf("entry %+v is not a setting change", e)
		}
		byKey[e.Tool] = e
	}
	if e := byKey["dictation.insert"]; e.Args != "auto → paste" || e.Actor != core.ActorAPI || e.Reason != "config.set" {
		t.Errorf("dictation.insert entry = %+v", e)
	}
	if e := byKey["ai.providers.claude.api_key"]; e.Args != "(unset) → [redacted]" {
		t.Errorf("secret entry = %+v, want the reference redacted", e)
	}
	if e := byKey["replies.speak"]; e.Confirmation != permissions.HowVoice || e.Actor != core.ActorOwner {
		t.Errorf("voice entry = %+v", e)
	}
}

// Spoken requests are the owner's; text from the socket is "api".
func TestActorFromTranscript(t *testing.T) {
	r := newRig(t, "", nil)
	bus.Publish(r.bus, core.Transcribed, core.Transcript{Turn: 7, Trigger: core.TriggerWake, Text: "footman open github"})
	bus.Publish(r.bus, core.RouteChosen, core.Routed{Turn: 7, Route: core.Route{Kind: core.RouteCommand, Text: "open github"}})
	bus.Publish(r.bus, core.FastPathDone, core.FastPathRan{Turn: 7, Rule: "open", Reply: "Opening github.", Confirmation: permissions.HowAuto})
	es := r.entries()
	if len(es) != 1 || es[0].Actor != core.ActorOwner || es[0].Reason != "open github" {
		t.Fatalf("log = %+v", es)
	}
}

// permissions.action_log = false records nothing.
func TestActionLogOff(t *testing.T) {
	r := newRig(t, "[permissions]\naction_log = false\n", nil)
	r.submit("open youtube")
	bus.Publish(r.bus, core.SettingChanged, core.SettingChange{Key: "dictation.insert", Old: "auto", New: "paste", Actor: core.ActorAPI})
	if es := r.entries(); len(es) != 0 {
		t.Errorf("log = %+v, want nothing", es)
	}
}

func TestConfigChanges(t *testing.T) {
	a := mustParse(t, "version = 1\n[plugins.mail]\nfolder = \"in\"\n")
	b := mustParse(t, "version = 1\n[dictation]\ninsert = \"type\"\n[plugins.mail]\nfolder = \"out\"\n")
	got := permissions.ConfigChanges(a, b)
	want := []core.SettingChange{
		{Key: "dictation.insert", Old: "auto", New: "type"},
		{Key: "plugins.mail.folder", Old: "in", New: "out"},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if c := permissions.ConfigChanges(a, a); len(c) != 0 {
		t.Errorf("no change gave %+v", c)
	}
}

// fakePruner records the ages it is asked to prune with.
type fakePruner struct {
	calls chan [2]time.Duration
}

func (f *fakePruner) Prune(_ context.Context, h, a time.Duration) (store.Pruned, error) {
	f.calls <- [2]time.Duration{h, a}
	return store.Pruned{}, nil
}

func TestRetain(t *testing.T) {
	cfg := mustParse(t, "version = 1\n[history]\nretention_days = 3\n[permissions]\naction_log_retention_days = 10\n")
	f := &fakePruner{calls: make(chan [2]time.Duration, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		permissions.Retain(ctx, f, func() *config.Config { return cfg }, 5*time.Millisecond, t.Logf)
	}()
	day := 24 * time.Hour
	for range 2 { // at once, then on the next tick
		select {
		case c := <-f.calls:
			if c != [2]time.Duration{3 * day, 10 * day} {
				t.Errorf("pruned with %v", c)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Retain did not prune")
		}
	}
	cancel()
	<-done
}

func TestRetentionAges(t *testing.T) {
	h, a := permissions.RetentionAges(config.Defaults())
	if h != 30*24*time.Hour || a != 90*24*time.Hour {
		t.Errorf("defaults: history %v, action log %v", h, a)
	}
	h, a = permissions.RetentionAges(mustParse(t, "version = 1\n[history]\nretention_days = 0\n[permissions]\naction_log_retention_days = 0\n"))
	if h <= 0 || h > time.Second || a != 0 {
		t.Errorf("zero days: history %v (want everything), action log %v (want kept)", h, a)
	}
}

// Real retention over the store: old entries go, recent ones stay.
func TestRetainPrunesStore(t *testing.T) {
	st := must(store.Open(context.Background(), filepath.Join(t.TempDir(), "footman.db")))
	defer func() { _ = st.Close() }()
	if _, err := st.AppendAction(context.Background(), store.ActionEntry{Actor: "owner", Action: "tool", Tool: "x"}); err != nil {
		t.Fatal(err)
	}
	cfg := mustParse(t, "version = 1\n[permissions]\naction_log_retention_days = 1\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one pass, then return
	permissions.Retain(ctx, st, func() *config.Config { return cfg }, time.Hour, t.Logf)
	if es, _ := st.RecentActions(context.Background(), 10); len(es) != 1 {
		t.Errorf("a fresh entry was pruned: %+v", es)
	}
}
