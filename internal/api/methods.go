package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
)

// Info describes the running daemon for status.get.
type Info struct {
	// Version is the footman build version.
	Version string
	// Started is when the daemon started.
	Started time.Time
	// DevFakes is true when the daemon was started with --dev-fakes.
	DevFakes bool
	// Implementations maps each area (platform, stt, tts, vad, wake, llm,
	// worker) to the registered implementation in use; "" means none.
	Implementations map[string]string
	// Socket and ConfigPath are the socket and config file in use.
	Socket, ConfigPath string
}

// Deps are what the v1 methods act on.
type Deps struct {
	Pipeline *core.Pipeline
	Bus      *bus.Bus
	Config   *ConfigFile
	Info     Info
	// Now is the clock for uptime (default time.Now).
	Now func() time.Time
}

// Register adds the v1 methods to s.
func Register(s *Server, d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	m := &methods{d: d}
	s.Handle("status.get", m.statusGet)
	s.Handle("events.subscribe", m.eventsSubscribe)
	s.Handle("events.unsubscribe", m.eventsUnsubscribe)
	s.Handle("say.text", m.sayText)
	s.Handle("jobs.list", m.jobsList)
	s.Handle("jobs.cancel", m.jobsCancel)
	s.Handle("config.get", m.configGet)
	s.Handle("config.set", m.configSet)
	s.Handle("config.schema", m.configSchema)
	s.Handle("system.stop_everything", m.stopEverything)
}

type methods struct {
	d Deps

	mu     sync.Mutex
	subSeq uint64
	subs   map[string]subscription
}

type subscription struct {
	conn    *Conn
	closeID uint64
	close   func()
}

// StatusResult is the result of status.get.
type StatusResult struct {
	Version         string            `json:"version"`
	API             string            `json:"api"`
	PID             int               `json:"pid"`
	StartedAt       time.Time         `json:"started_at"`
	UptimeSeconds   float64           `json:"uptime_seconds"`
	State           string            `json:"state"`
	Muted           bool              `json:"muted"`
	Jobs            []string          `json:"jobs"`
	DevFakes        bool              `json:"dev_fakes"`
	Implementations map[string]string `json:"implementations"`
	Socket          string            `json:"socket"`
	Config          string            `json:"config"`
}

func (m *methods) statusGet(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	if err := DecodeParams(params, &struct{}{}); err != nil {
		return nil, err
	}
	p := m.d.Pipeline
	impl := m.d.Info.Implementations
	if impl == nil {
		impl = map[string]string{}
	}
	jobs := p.Jobs()
	if jobs == nil {
		jobs = []string{}
	}
	return StatusResult{
		Version:         m.d.Info.Version,
		API:             APIVersion,
		PID:             os.Getpid(),
		StartedAt:       m.d.Info.Started,
		UptimeSeconds:   m.d.Now().Sub(m.d.Info.Started).Seconds(),
		State:           p.State().String(),
		Muted:           p.Muted(),
		Jobs:            jobs,
		DevFakes:        m.d.Info.DevFakes,
		Implementations: impl,
		Socket:          m.d.Info.Socket,
		Config:          m.d.Info.ConfigPath,
	}, nil
}

// SubscribeParams are the params of events.subscribe.
type SubscribeParams struct {
	// Topics are exact topic names, prefixes like "pipeline.*", or "*".
	// Empty means every topic.
	Topics []string `json:"topics,omitempty"`
	// Buffer is how many events may queue for this client before newer
	// ones are dropped (default 256).
	Buffer int `json:"buffer,omitempty"`
}

// SubscribeResult is the result of events.subscribe.
type SubscribeResult struct {
	Subscription string `json:"subscription"`
}

func (m *methods) eventsSubscribe(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p SubscribeParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	for _, t := range p.Topics {
		if t == "" || (strings.Contains(t, "*") && t != "*" && (!strings.HasSuffix(t, ".*") || strings.Count(t, "*") > 1)) {
			return nil, Errorf(CodeInvalidParams, "invalid topic pattern %q: use a topic name, a prefix like \"pipeline.*\", or \"*\"", t)
		}
	}
	if p.Buffer <= 0 {
		p.Buffer = 256
	}
	filter := topicFilter(p.Topics)
	sub := bus.SubscribeAll(m.d.Bus, p.Buffer)

	m.mu.Lock()
	m.subSeq++
	id := "sub-" + strconv.FormatUint(m.subSeq, 10)
	if m.subs == nil {
		m.subs = map[string]subscription{}
	}
	closeID := c.OnClose(func() { m.dropSub(id) })
	m.subs[id] = subscription{conn: c, closeID: closeID, close: sub.Close}
	m.mu.Unlock()

	// The response must reach the client before the first event, so the
	// forwarder starts sending only once this handler has returned and the
	// server has written the result. Events published meanwhile wait in the
	// subscription's buffer.
	ready := make(chan struct{})
	c.Go(func() {
		select {
		case <-ready:
		case <-c.Context().Done():
		}
		for ev := range sub.C() {
			if !filter.match(ev.Topic) {
				continue
			}
			e, err := toEvent(id, ev)
			if err != nil {
				continue
			}
			if c.Notify(EventMethod, e) != nil {
				// The connection is gone; its OnClose ends the subscription.
				for range sub.C() {
				}
				return
			}
		}
	})
	return deferredResult{v: SubscribeResult{Subscription: id}, after: func() { close(ready) }}, nil
}

// dropSub ends a subscription.
func (m *methods) dropSub(id string) bool {
	m.mu.Lock()
	s, ok := m.subs[id]
	delete(m.subs, id)
	m.mu.Unlock()
	if ok {
		s.close()
	}
	return ok
}

// UnsubscribeParams are the params of events.unsubscribe.
type UnsubscribeParams struct {
	Subscription string `json:"subscription"`
}

func (m *methods) eventsUnsubscribe(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p UnsubscribeParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	m.mu.Lock()
	s, ok := m.subs[p.Subscription]
	m.mu.Unlock()
	if !ok || s.conn != c {
		return nil, Errorf(CodeNotFound, "no subscription %q on this connection", p.Subscription)
	}
	c.Release(s.closeID)
	m.dropSub(p.Subscription)
	return struct{}{}, nil
}

// SayParams are the params of say.text.
type SayParams struct {
	Text string `json:"text"`
	// As is "command" (the default), "dictate" or "wake" (as if spoken
	// after the wake word, so "stop everything" is the break-glass stop).
	As string `json:"as,omitempty"`
}

func (m *methods) sayText(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p SayParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, Errorf(CodeInvalidParams, "text is empty")
	}
	var trig core.Trigger
	switch p.As {
	case "", "command":
		trig = core.TriggerCommand
	case "dictate":
		trig = core.TriggerDictate
	case "wake":
		trig = core.TriggerWake
	default:
		return nil, Errorf(CodeInvalidParams, "as must be command, dictate or wake, not %q", p.As)
	}
	err := m.d.Pipeline.Submit(ctx, core.Utterance{Trigger: trig, Text: p.Text})
	switch {
	case errors.Is(err, core.ErrBusy):
		return nil, Errorf(CodeBusy, "footman is busy with another utterance")
	case errors.Is(err, context.Canceled) && ctx.Err() == nil:
		return nil, Errorf(CodeFailed, "cancelled by a stop")
	case err != nil:
		return nil, err
	}
	return struct{}{}, nil
}

// JobInfo is one entry of jobs.list.
type JobInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// JobsResult is the result of jobs.list.
type JobsResult struct {
	Jobs []JobInfo `json:"jobs"`
}

func (m *methods) jobsList(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	if err := DecodeParams(params, &struct{}{}); err != nil {
		return nil, err
	}
	out := JobsResult{Jobs: []JobInfo{}}
	for _, id := range m.d.Pipeline.Jobs() {
		if j := m.d.Pipeline.Job(id); j != nil {
			out.Jobs = append(out.Jobs, JobInfo{ID: id, Status: j.Status().String()})
		}
	}
	return out, nil
}

// JobParams name one job.
type JobParams struct {
	ID string `json:"id"`
}

func (m *methods) jobsCancel(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p JobParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	j := m.d.Pipeline.Job(p.ID)
	if j == nil {
		return nil, Errorf(CodeNotFound, "no running job %q", p.ID)
	}
	j.Cancel()
	return struct{}{}, nil
}

// ConfigGetParams are the params of config.get.
type ConfigGetParams struct {
	// Key is a dotted key; empty returns every set value.
	Key string `json:"key,omitempty"`
}

// ConfigValue is one key's value.
type ConfigValue struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// ConfigValues is the result of config.get without a key.
type ConfigValues struct {
	Values map[string]any `json:"values"`
}

func (m *methods) configGet(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p ConfigGetParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	cfg := m.d.Config.Current()
	if p.Key == "" {
		return ConfigValues{Values: allValues(cfg)}, nil
	}
	if id, rest, ok := pluginKey(p.Key); ok {
		return ConfigValue{Key: p.Key, Value: cfg.Plugin(id)[rest]}, nil
	}
	if _, ok := config.Lookup(p.Key); !ok {
		return nil, Errorf(CodeNotFound, "unknown config key %q", p.Key)
	}
	return ConfigValue{Key: p.Key, Value: apiValue(cfg.Get(p.Key))}, nil
}

func pluginKey(key string) (id, rest string, ok bool) {
	after, ok := strings.CutPrefix(key, config.PluginsPrefix+".")
	if !ok {
		return "", "", false
	}
	id, rest, ok = strings.Cut(after, ".")
	return id, rest, ok && id != "" && rest != ""
}

// apiValue turns a config value into its JSON form (secret references as
// "keychain:<name>").
func apiValue(v any) any {
	if s, ok := v.(config.SecretRef); ok {
		return s.String()
	}
	return v
}

// allValues lists every value set in cfg by dotted key.
func allValues(cfg *config.Config) map[string]any {
	out := map[string]any{}
	for _, k := range config.Schema() {
		prefix, suffix, wild := strings.Cut(k.Path, ".*.")
		if !wild {
			if v := cfg.Get(k.Path); v != nil {
				out[k.Path] = apiValue(v)
			}
			continue
		}
		for _, name := range cfg.Names(prefix) {
			p := prefix + "." + name + "." + suffix
			if v := cfg.Get(p); v != nil {
				out[p] = apiValue(v)
			}
		}
	}
	for _, id := range cfg.PluginIDs() {
		for k, v := range cfg.Plugin(id) {
			out[config.PluginsPrefix+"."+id+"."+k] = v
		}
	}
	return out
}

// ConfigSetParams are the params of config.set.
type ConfigSetParams struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// ConfigSetResult is the result of config.set.
type ConfigSetResult struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	// Reload is "live" or "restart": whether the daemon must be restarted
	// for the change to take effect ("" for plugin keys).
	Reload string `json:"reload"`
}

func (m *methods) configSet(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	var p ConfigSetParams
	if err := DecodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Key == "" || len(p.Value) == 0 {
		return nil, Errorf(CodeInvalidParams, "key and value are required")
	}
	v, err := decodeValue(p.Value)
	if err != nil {
		return nil, Errorf(CodeInvalidParams, "value: %v", err)
	}
	cfg, err := m.d.Config.Set(p.Key, v)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
		return nil, err
	}
	res := ConfigSetResult{Key: p.Key}
	if id, rest, ok := pluginKey(p.Key); ok {
		res.Value = cfg.Plugin(id)[rest]
	} else {
		k, _ := config.Lookup(p.Key)
		res.Value = apiValue(cfg.Get(p.Key))
		res.Reload = string(k.Reload)
	}
	return res, nil
}

// decodeValue decodes a JSON value into the Go types config.Set takes:
// whole numbers become int64, others float64.
func decodeValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return fromNumbers(v)
}

func fromNumbers(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, nil
		}
		return x.Float64()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ev, err := fromNumbers(e)
			if err != nil {
				return nil, err
			}
			out[i] = ev
		}
		return out, nil
	case map[string]any:
		return nil, errors.New("tables can't be set as one value; set each key")
	case nil:
		return nil, errors.New("null is not a config value")
	}
	return v, nil
}

// SchemaResult is the result of config.schema.
type SchemaResult struct {
	Keys []config.Key `json:"keys"`
}

func (m *methods) configSchema(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	if err := DecodeParams(params, &struct{}{}); err != nil {
		return nil, err
	}
	return SchemaResult{Keys: config.Schema()}, nil
}

// StopResult is the result of system.stop_everything.
type StopResult struct {
	// Jobs lists the IDs of the cancelled jobs.
	Jobs []string `json:"jobs"`
	// Turns is how many in-flight utterances were cancelled.
	Turns int `json:"turns"`
}

func (m *methods) stopEverything(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
	if err := DecodeParams(params, &struct{}{}); err != nil {
		return nil, err
	}
	// Stop publishes its outcome before it returns; listen for it.
	sub := bus.Subscribe(m.d.Bus, core.StopDone, 16)
	defer sub.Close()
	m.d.Pipeline.Stop()
	res := StopResult{Jobs: []string{}}
	for {
		select {
		case ev, ok := <-sub.C():
			if !ok {
				return res, nil
			}
			if ev.Data.Source != core.StopCall {
				continue
			}
			if ev.Data.Jobs != nil {
				res.Jobs = slices.Clone(ev.Data.Jobs)
			}
			res.Turns = ev.Data.Turns
			return res, nil
		default:
			return res, nil
		}
	}
}

// ConfigFile is the daemon's config file and the config last loaded from it.
// It is safe for concurrent use.
type ConfigFile struct {
	path string

	mu  sync.Mutex
	cur *config.Config
}

// NewConfigFile wraps the config at path, already loaded as cfg.
func NewConfigFile(path string, cfg *config.Config) *ConfigFile {
	return &ConfigFile{path: path, cur: cfg}
}

// Path returns the file's path.
func (f *ConfigFile) Path() string { return f.path }

// Current returns the config last loaded.
func (f *ConfigFile) Current() *config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

// Replace swaps in a freshly loaded config, as after the file was edited by
// hand.
func (f *ConfigFile) Replace(cfg *config.Config) {
	f.mu.Lock()
	f.cur = cfg
	f.mu.Unlock()
}

// Set edits one key in the file, keeping comments, and reloads it.
func (f *ConfigFile) Set(key string, value any) (*config.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := config.SetFile(f.path, key, value); err != nil {
		return nil, err
	}
	cfg, err := config.Load(f.path)
	if err != nil {
		return nil, fmt.Errorf("reload after edit: %w", err)
	}
	f.cur = cfg
	return cfg, nil
}

// deferredResult is a handler result with a function the server runs once the
// response has been written.
type deferredResult struct {
	v     any
	after func()
}
