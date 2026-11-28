package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/plugins"
)

// ErrNeedsConfirmation is returned by DefaultGate for tools that need the
// user's confirmation, which it cannot ask for.
var ErrNeedsConfirmation = errors.New("needs confirmation, which is not available")

// Gate is the permission gate (design §29). Check returns nil to let a tool
// call run, or an error the AI is told about. untrusted is true once
// untrusted content has entered the current task's context.
type Gate interface {
	Check(ctx context.Context, spec plugins.ToolSpec, args json.RawMessage, untrusted bool) error
}

// Decider is a Gate that also says how it decided, for the action log:
// "auto", "click", "voice", "policy" or "timeout" (permissions.Decision.How).
// permissions.Gate implements it. For a Gate that does not, an allowed call
// is recorded as "auto" and a refused one as "policy".
type Decider interface {
	Gate
	Decide(ctx context.Context, spec plugins.ToolSpec, args json.RawMessage, untrusted bool) (how string, err error)
}

// How a gate decided, for gates that are not Deciders.
const (
	howAuto   = "auto"
	howPolicy = "policy"
)

// DefaultGate is the gate the pipeline uses when Deps.Gate is nil: the
// default policy (design §29) with nobody to ask. It runs Read and Act tools
// and refuses Destructive and Outward ones, which need confirmation; once
// untrusted content is in the task it refuses Act ones too (ADR-0047). The
// daemon uses permissions.Gate instead, which reads the policy from config
// and asks a Confirmer (ADR-0038).
type DefaultGate struct{}

// Check implements Gate.
func (g DefaultGate) Check(ctx context.Context, spec plugins.ToolSpec, args json.RawMessage, untrusted bool) error {
	_, err := g.Decide(ctx, spec, args, untrusted)
	return err
}

// Decide implements Decider.
func (DefaultGate) Decide(_ context.Context, spec plugins.ToolSpec, _ json.RawMessage, untrusted bool) (string, error) {
	switch spec.Permission {
	case plugins.Read:
		return howAuto, nil
	case plugins.Act:
		if untrusted {
			return howPolicy, fmt.Errorf("%s is an act action and untrusted content is in this task: %w", spec.Name, ErrNeedsConfirmation)
		}
		return howAuto, nil
	case plugins.Destructive, plugins.Outward:
		return howPolicy, fmt.Errorf("%s is a %s action: %w", spec.Name, spec.Permission, ErrNeedsConfirmation)
	}
	return howPolicy, fmt.Errorf("%s has invalid permission %v", spec.Name, spec.Permission)
}

// decide runs the gate and says how it decided.
func (p *Pipeline) decide(ctx context.Context, spec plugins.ToolSpec, args json.RawMessage, untrusted bool) (string, error) {
	if d, ok := p.d.Gate.(Decider); ok {
		return d.Decide(ctx, spec, args, untrusted)
	}
	if err := p.d.Gate.Check(ctx, spec, args, untrusted); err != nil {
		return howPolicy, err
	}
	return howAuto, nil
}

// StartTaskTool is the name of the built-in tool the AI calls to hand a long
// task to the background worker.
const StartTaskTool = "start_task"

var startTaskDef = llm.ToolDef{
	Name: StartTaskTool,
	Description: "Hand a long task (coding, research, anything that takes more than a few seconds) " +
		"to a background worker. Footman replies at once and reports back when it finishes.",
	Schema: json.RawMessage(`{"type":"object","properties":{` +
		`"prompt":{"type":"string","description":"What to do, in full."},` +
		`"dir":{"type":"string","description":"Project directory to work in, if any."}},` +
		`"required":["prompt"]}`),
}

// converse runs the AI with tools until it replies, hands off a long task, or
// runs out of rounds.
func (p *Pipeline) converse(ctx context.Context, id uint64, text string) (string, error) {
	tools := map[string]plugins.Tool{}
	var defs []llm.ToolDef
	for _, t := range p.allTools() {
		s := t.Spec()
		tools[s.Name] = t
		defs = append(defs, llm.ToolDef{Name: s.Name, Description: s.Description, Schema: s.Schema})
	}
	if p.d.Worker != nil {
		defs = append(defs, startTaskDef)
	}
	web, webHow := p.webSearching(ctx)
	system, withSystem := p.systemPrompt(web)
	msgs := []llm.Message{{Role: llm.User, Text: p.userTurn(text, withSystem)}}
	untrusted := false
	if followUp, _ := ctx.Value(followUpKey{}).(bool); followUp {
		// The answer to a question the AI just asked: the same
		// conversation, and the same untrusted content, if any (ADR-0047).
		if c := p.takeConvo(); c != nil {
			msgs = append(c.msgs, llm.Message{Role: llm.User, Text: text})
			untrusted = c.untrusted
		}
	}
	for range p.cfg.MaxToolRounds {
		p.setState(id, Thinking)
		s, err := p.d.LLM.Chat(ctx, llm.Request{Model: p.cfg.Model, System: system, Messages: msgs, Tools: defs, WebSearch: web})
		if err != nil {
			return "", fmt.Errorf("ai: %w", err)
		}
		sp := speakerFrom(ctx)
		r, err := collectStreaming(s, sp, func() { markToken(ctx) })
		if err != nil {
			return "", fmt.Errorf("ai: %w", err)
		}
		msgs = append(msgs, r.Message)
		if r.Untrusted {
			// Search results are web content (ADR-0015, ADR-0044): from
			// here on, outward and destructive actions need confirmation,
			// whatever the config says.
			untrusted = true
		}
		if r.Usage.WebSearches > 0 {
			bus.Publish(p.d.Bus, ToolCalled, ToolRan{Turn: id, Name: WebSearchTool, Result: fmt.Sprintf("%d web searches", r.Usage.WebSearches), Confirmation: webHow})
		}
		if len(r.Message.ToolCalls) == 0 {
			if r.Stop == llm.StopPause {
				continue // a long server-side search paused; send it back to go on
			}
			if sp != nil {
				sp.streamedFinal = true
			}
			if endsWithQuestion(r.Message.Text) {
				p.stashConvo(msgs, untrusted) // the answer continues this conversation
			}
			return r.Message.Text, nil
		}
		if sp != nil {
			sp.Break() // what came before a tool call is its own sentence
		}
		for _, call := range r.Message.ToolCalls {
			if call.Name == StartTaskTool && p.d.Worker != nil {
				return p.startTask(id, call, untrusted)
			}
			p.setState(id, RunningTool)
			res, how := p.runTool(ctx, tools[call.Name], call, untrusted)
			untrusted = untrusted || res.Untrusted
			bus.Publish(p.d.Bus, ToolCalled, ToolRan{Turn: id, Name: call.Name, Args: call.Args, Result: res.Text, IsError: res.IsError, Confirmation: how})
			msgs = append(msgs, llm.Message{Role: llm.ToolResult, ToolCallID: call.ID, Text: res.Text, IsError: res.IsError})
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	return p.line(LineError, "Sorry, I couldn't finish that."), nil
}

// collectStreaming reads s to the end like llm.Collect, handing each text
// delta to sp (if any) as it arrives so speech starts before the reply ends.
// onFirst is called at the first text.
func collectStreaming(s llm.Stream, sp *speaker, onFirst func()) (llm.Reply, error) {
	defer func() { _ = s.Close() }()
	r := llm.Reply{Message: llm.Message{Role: llm.Assistant}}
	var text strings.Builder
	for {
		e, err := s.Recv()
		if errors.Is(err, io.EOF) {
			r.Message.Text = text.String()
			return r, nil
		}
		if err != nil {
			return r, err
		}
		switch e.Kind {
		case llm.TextDelta:
			if text.Len() == 0 && e.Text != "" {
				onFirst()
			}
			text.WriteString(e.Text)
			if sp != nil {
				sp.Feed(e.Text)
			}
		case llm.ToolCallEvent:
			r.Message.ToolCalls = append(r.Message.ToolCalls, e.ToolCall)
		case llm.Done:
			r.Stop, r.Usage, r.Untrusted = e.Stop, e.Usage, e.Untrusted
			r.Message.Raw = e.Raw
		}
	}
}

// runTool runs one tool call through the gate and returns its result and how
// the gate decided ("" for an unknown tool). Failures become error results
// the AI can see and recover from.
func (p *Pipeline) runTool(ctx context.Context, t plugins.Tool, call llm.ToolCall, untrusted bool) (plugins.Result, string) {
	if t == nil {
		return plugins.Result{Text: fmt.Sprintf("unknown tool %q", call.Name), IsError: true}, ""
	}
	how, err := p.decide(ctx, t.Spec(), call.Args, untrusted)
	if err != nil {
		return plugins.Result{Text: "refused: " + err.Error(), IsError: true}, how
	}
	res, err := t.Run(ctx, call.Args)
	if err != nil {
		return plugins.Result{Text: err.Error(), IsError: true, Untrusted: res.Untrusted}, how
	}
	return res, how
}

// startTask hands the AI's start_task call to the worker and ends the turn at
// once; the job reports progress on JobUpdated.
func (p *Pipeline) startTask(turn uint64, call llm.ToolCall, untrusted bool) (string, error) {
	var args struct {
		Prompt string `json:"prompt"`
		Dir    string `json:"dir"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil || args.Prompt == "" {
		return "Sorry, I couldn't work out what that task was.", nil
	}
	id, err := p.StartJob(jobs.Task{Prompt: args.Prompt, Dir: args.Dir, Untrusted: untrusted})
	ev := ToolRan{Turn: turn, Name: call.Name, Args: call.Args, Result: id, Confirmation: howAuto}
	if err != nil {
		ev.Result, ev.IsError = err.Error(), true
	}
	bus.Publish(p.d.Bus, ToolCalled, ev)
	if err != nil {
		return "", fmt.Errorf("start job: %w", err)
	}
	return "On it. I'll let you know when it's done.", nil
}

// allTools returns the configured tools plus the built-in fast-path actions
// (open_url, open_app, quick_action) and, when the platform can, the
// read-only system_status, unless a configured tool already uses the name.
func (p *Pipeline) allTools() []plugins.Tool {
	all := append([]plugins.Tool(nil), p.d.Tools...)
	have := map[string]bool{}
	for _, t := range all {
		have[t.Spec().Name] = true
	}
	builtin := []plugins.Tool{openURLTool{p}, openAppTool{p}, quickActionTool{p}}
	if _, ok := p.d.Platform.(platform.SystemInfo); ok {
		builtin = append(builtin, systemStatusTool{p})
	}
	for _, t := range builtin {
		if !have[t.Spec().Name] {
			all = append(all, t)
		}
	}
	return all
}

// openURLTool and openAppTool expose the fast-path "open" action to the AI.
type openURLTool struct{ p *Pipeline }

func (openURLTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name:        "open_url",
		Description: "Open a web page in the user's default browser, for example when they say \"open YouTube\".",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"url":{"type":"string","description":"Full URL, such as https://www.youtube.com."}},"required":["url"]}`),
		Permission: plugins.Act,
	}
}

func (t openURLTool) Run(ctx context.Context, args json.RawMessage) (plugins.Result, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.URL == "" {
		return plugins.Result{Text: "url is required", IsError: true}, nil
	}
	u, ok := asURL(a.URL)
	if !ok {
		return plugins.Result{Text: "not a valid URL: " + a.URL, IsError: true}, nil
	}
	if err := t.p.d.Platform.Launcher().OpenURL(ctx, u); err != nil {
		return plugins.Result{}, err
	}
	return plugins.Result{Text: "opened " + u}, nil
}

type openAppTool struct{ p *Pipeline }

func (openAppTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name:        "open_app",
		Description: "Start an application by name.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"app":{"type":"string","description":"Application name or desktop ID."}},"required":["app"]}`),
		Permission: plugins.Act,
	}
}

func (t openAppTool) Run(ctx context.Context, args json.RawMessage) (plugins.Result, error) {
	var a struct {
		App string `json:"app"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.App == "" {
		return plugins.Result{Text: "app is required", IsError: true}, nil
	}
	if err := t.p.d.Platform.Launcher().OpenApp(ctx, a.App); err != nil {
		return plugins.Result{}, err
	}
	return plugins.Result{Text: "started " + a.App}, nil
}

// quickActionTool exposes the volume and media fast-path rules to the AI, for
// commands the fast path didn't recognize as said ("turn it down a bit").
type quickActionTool struct{ p *Pipeline }

func (quickActionTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name: "quick_action",
		Description: "Change the volume or control music and video playback. Pass one of: " +
			"\"volume up\", \"volume down\", \"mute\", \"unmute\", \"set volume to N percent\", " +
			"\"play\", \"pause\", \"next\", \"previous\".",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"action":{"type":"string","description":"The action, such as \"volume down\" or \"pause\"."}},"required":["action"]}`),
		Permission: plugins.Act,
	}
}

func (t quickActionTool) Run(ctx context.Context, args json.RawMessage) (plugins.Result, error) {
	var a struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Action == "" {
		return plugins.Result{Text: "action is required", IsError: true}, nil
	}
	for _, rule := range []FastRule{VolumeRule(), MediaRule()} {
		if run, ok := rule.Match(a.Action); ok {
			reply, err := run(ctx, t.p)
			if err != nil {
				return plugins.Result{}, err
			}
			return plugins.Result{Text: reply}, nil
		}
	}
	return plugins.Result{Text: "unknown action: " + a.Action, IsError: true}, nil
}
