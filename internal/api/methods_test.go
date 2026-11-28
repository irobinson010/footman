package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/jobs"
	jobsfake "github.com/irobinson010/footman/internal/jobs/fake"
	"github.com/irobinson010/footman/internal/llm"
	platformfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
	speechfake "github.com/irobinson010/footman/internal/speech/fake"
)

// hangLLM is a provider whose Chat blocks until its context ends, to keep
// the pipeline busy.
type hangLLM struct{ entered chan struct{} }

func (h *hangLLM) Capabilities() llm.Caps { return llm.Caps{Tools: true} }

func (h *hangLLM) Chat(ctx context.Context, req llm.Request) (llm.Stream, error) {
	h.entered <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

type fixture struct {
	t        *testing.T
	c        *Client
	pipeline *core.Pipeline
	platform *platformfake.Platform
	worker   *jobsfake.Worker
	llm      *hangLLM
	cfgPath  string
}

const testConfig = `# my settings
version = 1

[dictation]
insert = "auto" # how text goes in
`

func newFixture(t *testing.T) *fixture {
	t.Helper()
	plat, err := platformfake.New(registry.Map(nil))
	if err != nil {
		t.Fatal(err)
	}
	stt, _ := speechfake.NewSTT(registry.Map(nil))
	worker, _ := jobsfake.New(registry.Map(nil))
	model := &hangLLM{entered: make(chan struct{}, 1)}
	b := bus.New()
	p, err := core.New(core.Deps{Bus: b, Platform: plat, STT: stt, LLM: model, Worker: worker}, core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	s := NewServer()
	Register(s, Deps{
		Pipeline: p, Bus: b, Config: NewConfigFile(cfgPath, cfg),
		Info: Info{Version: "test", Started: started, Implementations: map[string]string{"platform": "fake"}, STTPath: func() string { return "sherpa-parakeet parakeet-tdt-0.6b-v2 on cpu (test)" }, Socket: "/run/test.sock", ConfigPath: cfgPath},
		Now:  func() time.Time { return started.Add(90 * time.Second) },
	})
	a, bconn := net.Pipe()
	s.ServeConn(a)
	c := NewClient(bconn)
	t.Cleanup(func() {
		_ = c.Close()
		s.Close()
		p.Close()
		b.Close()
	})
	return &fixture{t: t, c: c, pipeline: p, platform: plat, worker: worker, llm: model, cfgPath: cfgPath}
}

func (f *fixture) call(method string, params, result any) error {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.c.Call(ctx, method, params, result)
}

func (f *fixture) mustCall(method string, params, result any) {
	f.t.Helper()
	if err := f.call(method, params, result); err != nil {
		f.t.Fatalf("%s: %v", method, err)
	}
}

// next returns the next event, failing the test if none arrives in time.
func (f *fixture) next() Event {
	f.t.Helper()
	select {
	case n, ok := <-f.c.Notifications():
		if !ok {
			f.t.Fatal("connection closed while waiting for an event")
		}
		if n.Method != EventMethod {
			f.t.Fatalf("notification method %q", n.Method)
		}
		var ev Event
		if err := json.Unmarshal(n.Params, &ev); err != nil {
			f.t.Fatal(err)
		}
		return ev
	case <-time.After(5 * time.Second):
		f.t.Fatal("no event within 5s")
	}
	return Event{}
}

// nextOn returns the next event on topic, skipping others.
func (f *fixture) nextOn(topic string) Event {
	f.t.Helper()
	for {
		if ev := f.next(); ev.Topic == topic {
			return ev
		}
	}
}

func code(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func TestStatusGet(t *testing.T) {
	f := newFixture(t)
	var s StatusResult
	f.mustCall("status.get", nil, &s)
	if s.Version != "test" || s.API != "v1" || s.State != "idle" || s.Muted || len(s.Jobs) != 0 ||
		s.UptimeSeconds != 90 || s.Implementations["platform"] != "fake" || s.STTPath != "sherpa-parakeet parakeet-tdt-0.6b-v2 on cpu (test)" || s.Socket != "/run/test.sock" || s.PID != os.Getpid() {
		t.Errorf("status = %+v", s)
	}
	if err := f.call("status.get", map[string]int{"x": 1}, nil); code(err) != CodeInvalidParams {
		t.Errorf("status.get with params = %v, want invalid params", err)
	}
}

func TestSayTextStreamsPipelineStates(t *testing.T) {
	f := newFixture(t)
	var sub SubscribeResult
	f.mustCall("events.subscribe", SubscribeParams{Topics: []string{"pipeline.state", "pipeline.fast_path"}}, &sub)
	if sub.Subscription == "" {
		t.Fatal("no subscription id")
	}

	f.mustCall("say.text", SayParams{Text: "open youtube"}, nil)
	if got := f.platform.Launcher().(*platformfake.Launcher).URLs(); !slices.Equal(got, []string{"https://www.youtube.com"}) {
		t.Errorf("opened %v", got)
	}

	want := []string{
		`pipeline.state {"from":"idle","to":"routing","turn":1}`,
		`pipeline.state {"from":"routing","to":"acknowledging","turn":1}`,
		`pipeline.state {"from":"acknowledging","to":"fast_path","turn":1}`,
		`pipeline.fast_path {"turn":1,"rule":"open","reply":"Opening youtube.","confirmation":"auto"}`,
		`pipeline.state {"from":"fast_path","to":"replying","turn":1}`,
		`pipeline.state {"from":"replying","to":"idle","turn":0}`,
	}
	var last uint64
	for i, w := range want {
		ev := f.next()
		if got := ev.Topic + " " + string(ev.Data); got != w {
			t.Errorf("event %d = %s, want %s", i, got, w)
		}
		if ev.Subscription != sub.Subscription || ev.Seq <= last || ev.At.IsZero() {
			t.Errorf("event %d header = %+v", i, ev)
		}
		last = ev.Seq
	}

	// Dictation types at the cursor.
	f.mustCall("say.text", SayParams{Text: "hello there", As: "dictate"}, nil)
	f.nextOn("pipeline.state")
	if got := f.platform.Input().(*platformfake.Input).Typed(); !slices.Equal(got, []string{"hello there"}) {
		t.Errorf("typed %q", got)
	}

	if err := f.call("say.text", SayParams{Text: "  "}, nil); code(err) != CodeInvalidParams {
		t.Errorf("empty text = %v", err)
	}
	if err := f.call("say.text", SayParams{Text: "hi", As: "shout"}, nil); code(err) != CodeInvalidParams {
		t.Errorf("bad as = %v", err)
	}
}

func TestEventsUnsubscribe(t *testing.T) {
	f := newFixture(t)
	var first, second SubscribeResult
	f.mustCall("events.subscribe", SubscribeParams{Topics: []string{"pipeline.fast_path"}}, &first)
	f.mustCall("say.text", SayParams{Text: "open github"}, nil)
	if ev := f.next(); ev.Subscription != first.Subscription {
		t.Fatalf("event %+v", ev)
	}
	f.mustCall("events.unsubscribe", UnsubscribeParams(first), nil)
	if err := f.call("events.unsubscribe", UnsubscribeParams(first), nil); code(err) != CodeNotFound {
		t.Errorf("second unsubscribe = %v, want not found", err)
	}

	f.mustCall("events.subscribe", SubscribeParams{Topics: []string{"pipeline.*"}}, &second)
	f.mustCall("say.text", SayParams{Text: "open google"}, nil)
	for range 6 { // 5 state changes and fast_path, then the route event
		if ev := f.next(); ev.Subscription != second.Subscription {
			t.Fatalf("event for %s after unsubscribing: %+v", ev.Subscription, ev)
		}
	}

	for _, bad := range []string{"", "pipe*", "*.state", "a.*.*"} {
		if err := f.call("events.subscribe", SubscribeParams{Topics: []string{bad}}, nil); code(err) != CodeInvalidParams {
			t.Errorf("topic %q = %v, want invalid params", bad, err)
		}
	}
}

func TestBusyAndStopEverything(t *testing.T) {
	f := newFixture(t)
	f.worker.Queue(jobsfake.Outcome{Hold: true})
	jobID, err := f.pipeline.StartJob(jobs.Task{Prompt: "refactor"})
	if err != nil {
		t.Fatal(err)
	}
	var sub SubscribeResult
	f.mustCall("events.subscribe", SubscribeParams{Topics: []string{"pipeline.stop", "pipeline.mic"}}, &sub)

	// A command the AI is thinking about keeps the pipeline busy.
	first := make(chan error, 1)
	go func() { first <- f.call("say.text", SayParams{Text: "what is the weather"}, nil) }()
	<-f.llm.entered
	if err := f.call("say.text", SayParams{Text: "open youtube"}, nil); code(err) != CodeBusy {
		t.Errorf("say while busy = %v, want busy", err)
	}

	var res StopResult
	f.mustCall("system.stop_everything", nil, &res)
	if !slices.Equal(res.Jobs, []string{jobID}) || res.Turns != 1 {
		t.Errorf("stop result = %+v, want job %s and 1 turn", res, jobID)
	}
	if err := <-first; code(err) != CodeFailed || !strings.Contains(err.Error(), "stop") {
		t.Errorf("interrupted say = %v, want cancelled by a stop", err)
	}
	if ev := f.next(); ev.Topic != "pipeline.mic" || string(ev.Data) != `{"mic":"off"}` {
		t.Errorf("event = %s %s", ev.Topic, ev.Data)
	}
	if ev := f.next(); ev.Topic != "pipeline.stop" || string(ev.Data) != `{"source":"call","jobs":["`+jobID+`"],"turns":1}` {
		t.Errorf("event = %s %s", ev.Topic, ev.Data)
	}
	var s StatusResult
	f.mustCall("status.get", nil, &s)
	if !s.Muted || s.State != "idle" {
		t.Errorf("status after stop = %+v", s)
	}

	// The spoken stop phrase works through say.text too, as a wake-word
	// utterance, and only then.
	f.mustCall("say.text", SayParams{Text: "footman, stop everything", As: "wake"}, nil)
	if ev := f.nextOn("pipeline.stop"); string(ev.Data) != `{"source":"voice","jobs":[],"turns":0}` {
		t.Errorf("voice stop = %s", ev.Data)
	}
}

func TestStatusReportsMicrophone(t *testing.T) {
	f := newFixture(t)
	// The fixture never runs the microphone loop, so it is off.
	var raw json.RawMessage
	f.mustCall("status.get", nil, &raw)
	if !strings.Contains(string(raw), `"microphone":{"state":"off"}`) {
		t.Errorf("status = %s, want the microphone reported as off", raw)
	}
}

func TestJobs(t *testing.T) {
	f := newFixture(t)
	f.worker.Queue(jobsfake.Outcome{Hold: true})
	id, err := f.pipeline.StartJob(jobs.Task{Prompt: "write tests"})
	if err != nil {
		t.Fatal(err)
	}
	var list JobsResult
	f.mustCall("jobs.list", nil, &list)
	if len(list.Jobs) != 1 || list.Jobs[0] != (JobInfo{ID: id, Status: "running"}) {
		t.Errorf("jobs = %+v", list)
	}
	if err := f.call("jobs.cancel", JobParams{ID: "job-99"}, nil); code(err) != CodeNotFound {
		t.Errorf("cancel unknown = %v", err)
	}
	f.mustCall("jobs.cancel", JobParams{ID: id}, nil)
	j := f.worker.Jobs()[0]
	if _, err := j.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Errorf("job ended with %v, want cancelled", err)
	}
}

func TestConfigMethods(t *testing.T) {
	f := newFixture(t)
	var v ConfigValue
	f.mustCall("config.get", ConfigGetParams{Key: "dictation.insert"}, &v)
	if v.Value != "auto" {
		t.Errorf("dictation.insert = %v", v.Value)
	}
	if err := f.call("config.get", ConfigGetParams{Key: "no.such"}, nil); code(err) != CodeNotFound {
		t.Errorf("unknown key = %v", err)
	}

	var set ConfigSetResult
	f.mustCall("config.set", map[string]any{"key": "dictation.insert", "value": "paste"}, &set)
	if set.Value != "paste" || set.Reload != "live" {
		t.Errorf("set = %+v", set)
	}
	f.mustCall("config.set", map[string]any{"key": "jobs.max_concurrent", "value": 3}, &set)
	f.mustCall("config.set", map[string]any{"key": "ai.budget.claude.warn_at", "value": 1}, nil)
	f.mustCall("config.set", map[string]any{"key": "context.local", "value": []string{"clipboard"}}, nil)
	f.mustCall("config.set", map[string]any{"key": "ai.providers.claude.api_key", "value": "keychain:claude"}, nil)
	f.mustCall("config.set", map[string]any{"key": "plugins.troupe.url", "value": "https://x"}, nil)
	for _, bad := range []map[string]any{
		{"key": "dictation.insert", "value": "shout"},
		{"key": "jobs.max_concurrent", "value": 1.5},
		{"key": "ai.providers.claude.api_key", "value": "sk-secret"},
		{"key": "nope", "value": 1},
		{"key": "dictation.insert", "value": nil},
		{"key": "dictation.insert"},
	} {
		if err := f.call("config.set", bad, nil); code(err) != CodeInvalidParams {
			t.Errorf("config.set %v = %v, want invalid params", bad, err)
		}
	}

	src, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my settings", `insert = "paste" # how text goes in`, "max_concurrent = 3"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("config file lacks %q:\n%s", want, src)
		}
	}

	var all ConfigValues
	f.mustCall("config.get", nil, &all)
	for k, want := range map[string]any{
		"dictation.insert":            "paste",
		"jobs.max_concurrent":         float64(3),
		"ai.budget.claude.warn_at":    float64(1),
		"ai.providers.claude.api_key": "keychain:claude",
		"plugins.troupe.url":          "https://x",
		"user.language":               "en",
	} {
		if all.Values[k] != want {
			t.Errorf("config.get %s = %#v, want %#v", k, all.Values[k], want)
		}
	}
	if got := all.Values["context.local"].([]any); len(got) != 1 || got[0] != "clipboard" {
		t.Errorf("context.local = %v", got)
	}
	f.mustCall("config.get", ConfigGetParams{Key: "plugins.troupe.url"}, &v)
	if v.Value != "https://x" {
		t.Errorf("plugin value = %v", v.Value)
	}

	var schema SchemaResult
	f.mustCall("config.schema", nil, &schema)
	if len(schema.Keys) != len(config.Schema()) || schema.Keys[0].Path != "version" {
		t.Errorf("schema has %d keys", len(schema.Keys))
	}
}
