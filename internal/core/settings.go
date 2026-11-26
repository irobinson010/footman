package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/plugins"
)

// SettingsStore is the config the settings-by-voice feature reads and edits.
// api.ConfigFile satisfies it: Set edits the file with the comment-preserving
// editor and reloads it, and the config watcher then applies live changes.
type SettingsStore interface {
	Current() *config.Config
	Set(key string, value any) (*config.Config, error)
}

// ConfirmRequest asks the user to confirm, with a physical click in the web
// UI, tray or CLI, a change that must never be made by voice (design §17,
// §29). Nothing has been changed when it is published.
type ConfirmRequest struct {
	// ID identifies the request.
	ID string
	// Key is the setting and Value the value that was asked for.
	Key   string
	Value string `json:",omitempty"`
	// Reason says why voice was not enough.
	Reason string
}

// SettingsConfirm is published when a setting change needs a click.
var SettingsConfirm = bus.NewTopic[ConfirmRequest]("settings.confirm")

// QuietKey is the setting behind "quiet mode": replies are never spoken.
const (
	QuietKey   = "replies.speak"
	quietOn    = "never"
	quietOff   = "when-voice"
	settingsID = "settings-"
)

// Settings reads and changes settings by voice. The fast-path rule and the AI
// tools share it, so both follow the same policy.
type Settings struct {
	Store SettingsStore
	// Bus receives SettingsConfirm requests. May be nil.
	Bus *bus.Bus

	seq atomic.Uint64
}

// Status is what happened to a settings request.
type Status int

// Settings request outcomes.
const (
	// Changed: the value was applied (or was already set).
	Changed Status = iota + 1
	// Invalid: unknown setting or bad value; nothing changed.
	Invalid
	// Refused: not allowed by voice; a click confirmation was requested.
	Refused
)

// Get answers "what is my <setting>".
func (s *Settings) Get(path string) (string, Status) {
	k, ok := config.Lookup(path)
	if !ok || strings.Contains(k.Path, "*") {
		return s.unknown(path), Invalid
	}
	if k.Type == config.Secret {
		return fmt.Sprintf("%s holds a secret, so I won't read it out.", spoken(k.Path)), Refused
	}
	v := s.Store.Current().Get(path)
	if v == nil {
		return fmt.Sprintf("%s isn't set.", capital(spoken(path))), Changed
	}
	return fmt.Sprintf("%s is %s.", capital(spoken(path)), show(v)), Changed
}

// Set applies "set <path> to <text>" under the voice policy.
func (s *Settings) Set(path, text string) (string, Status) {
	k, verdict, why := config.VoicePolicy(path)
	switch verdict {
	case config.VoiceUnknown:
		return s.unknown(path), Invalid
	case config.VoiceNeedsClick:
		return s.refuse(k, text, why), Refused
	}
	v, err := config.ParseValue(path, text)
	if err != nil {
		return fmt.Sprintf("I can't set that: %v.", err), Invalid
	}
	old := s.Store.Current().Get(path)
	if _, err := s.Store.Set(path, v); err != nil {
		return fmt.Sprintf("I couldn't change %s: %v.", spoken(path), err), Invalid
	}
	note := ""
	if k.Reload == config.Restart {
		note = " It takes effect after Footman restarts."
	}
	if fmt.Sprint(old) == fmt.Sprint(v) {
		return fmt.Sprintf("%s was already %s.", capital(spoken(path)), show(v)), Changed
	}
	return fmt.Sprintf("%s changed from %s to %s.%s", capital(spoken(path)), show(old), show(v), note), Changed
}

// refuse explains why voice can't make the change and asks for a click.
func (s *Settings) refuse(k config.Key, text, why string) string {
	id := fmt.Sprintf("%s%d", settingsID, s.seq.Add(1))
	bus.Publish(s.Bus, SettingsConfirm, ConfirmRequest{ID: id, Key: k.Path, Value: text, Reason: why})
	return fmt.Sprintf("I won't change %s by voice because %s. I've asked for a confirmation: click it in the web UI or tray to go ahead.",
		spoken(k.Path), why)
}

func (s *Settings) unknown(path string) string {
	return fmt.Sprintf("I don't have a setting called %s.", spoken(path))
}

// List returns the keys that may be changed by voice, with their values.
func (s *Settings) List() []string {
	var out []string
	cfg := s.Store.Current()
	for _, k := range config.Schema() {
		if strings.Contains(k.Path, "*") {
			continue
		}
		if _, v, _ := config.VoicePolicy(k.Path); v == config.VoiceAllowed {
			out = append(out, fmt.Sprintf("%s = %s", k.Path, show(cfg.Get(k.Path))))
		}
	}
	return out
}

func show(v any) string {
	switch t := v.(type) {
	case nil:
		return "not set"
	case string:
		if t == "" {
			return "empty"
		}
		return t
	case bool:
		if t {
			return "on"
		}
		return "off"
	case []string:
		if len(t) == 0 {
			return "empty"
		}
		return strings.Join(t, ", ")
	}
	return fmt.Sprint(v)
}

func spoken(path string) string {
	return strings.NewReplacer(".", " ", "_", " ").Replace(path)
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// aliases are spoken names that differ from the key path.
var aliases = map[string]string{
	"dictate key":         "input.dictate.key",
	"dictation key":       "input.dictate.key",
	"dictation hotkey":    "input.dictate.key",
	"command key":         "input.command.key",
	"command hotkey":      "input.command.key",
	"wake word":           "input.wake_word",
	"wake word threshold": "speech.wake.threshold",
	"wake word boost":     "speech.wake.boost",
	"quiet hours":         "replies.quiet_hours",
	"my name":             "user.name",
	"name":                "user.name",
	"language":            "user.language",
	"default persona":     "personas.default",
	"transcript history":  "history.transcripts",
}

// resolve finds the setting a spoken name refers to: an alias, the full key
// path ("history retention days") or the path without its table
// ("retention days"). Names that fit several keys resolve to none.
func resolve(name string) (string, bool) {
	if p, ok := aliases[name]; ok {
		return p, true
	}
	found := ""
	for _, k := range config.Schema() {
		if strings.Contains(k.Path, "*") {
			continue
		}
		full := spoken(k.Path)
		short := full
		if i := strings.IndexByte(full, ' '); i > 0 {
			short = full[i+1:]
		}
		if name == full {
			return k.Path, true
		}
		if name == short {
			if found != "" {
				return "", false
			}
			found = k.Path
		}
	}
	return found, found != ""
}

// SettingsRule is the fast-path rule for settings: "turn on quiet mode",
// "what is my dictate key", "turn off the wake word" and "set <setting> to
// <value>". Names that are not settings are not matched, so those commands
// fall through to the next rule or the AI.
func SettingsRule(s *Settings) FastRule {
	return FastRule{Name: "settings", Permission: plugins.Act, Match: func(text string) (FastAction, bool) {
		var toks, words []string
		for _, t := range strings.Fields(text) {
			if n := dictation.Norm(t); n != "" {
				toks, words = append(toks, t), append(words, n)
			}
		}
		words = dropWords(words, "please")
		if len(words) < 2 {
			return nil, false
		}
		run := func(f func() string) (FastAction, bool) {
			return func(context.Context, *Pipeline) (string, error) { return f(), nil }, true
		}
		clean := func(w []string) string { return strings.Join(dropWords(w, "the", "my", "your"), " ") }

		// quiet mode
		if m := strings.Join(words, " "); strings.Contains(m, "quiet mode") {
			rest := strings.Join(dropWords(words, "turn", "switch", "the", "mode", "quiet", "go", "into"), " ")
			var on bool
			switch rest {
			case "on", "enable", "start", "enter":
				on = true
			case "off", "disable", "stop", "leave", "exit", "end":
			default:
				return nil, false
			}
			return run(func() string {
				v := quietOff
				if on {
					v = quietOn
				}
				reply, st := s.Set(QuietKey, v)
				if st != Changed {
					return reply
				}
				if on {
					return "Quiet mode is on. I won't speak replies."
				}
				return "Quiet mode is off. Replies are spoken when you speak."
			})
		}

		switch words[0] {
		case "what", "whats", "get", "show", "tell":
			rest := words[1:]
			if words[0] == "what" && len(rest) > 0 && (rest[0] == "is" || rest[0] == "are") {
				rest = rest[1:]
			}
			if words[0] == "tell" && len(rest) > 1 && rest[0] == "me" {
				rest = rest[1:]
			}
			if path, ok := resolve(clean(rest)); ok {
				return run(func() string { r, _ := s.Get(path); return r })
			}
		case "turn", "switch", "enable", "disable":
			rest := words[1:]
			on := words[0] == "enable"
			if words[0] == "turn" || words[0] == "switch" {
				// "turn on X", "turn X off"
				switch {
				case len(rest) > 1 && (rest[0] == "on" || rest[0] == "off"):
					on, rest = rest[0] == "on", rest[1:]
				case len(rest) > 1 && (rest[len(rest)-1] == "on" || rest[len(rest)-1] == "off"):
					on, rest = rest[len(rest)-1] == "on", rest[:len(rest)-1]
				default:
					return nil, false
				}
			}
			path, ok := resolve(clean(rest))
			if !ok {
				return nil, false
			}
			if k, _ := config.Lookup(path); k.Type != config.Bool {
				return nil, false
			}
			val := "off"
			if on {
				val = "on"
			}
			return run(func() string { r, _ := s.Set(path, val); return r })
		case "set", "change":
			to := -1
			for i, w := range words {
				if w == "to" && i > 1 {
					to = i
					break
				}
			}
			if to < 0 || to == len(words)-1 {
				return nil, false
			}
			path, ok := resolve(clean(words[1:to]))
			if !ok {
				return nil, false
			}
			value := strings.Join(toks[to+1:], " ")
			return run(func() string { r, _ := s.Set(path, value); return r })
		}
		return nil, false
	}}
}

// SettingsTools returns the get_setting, set_setting and list_settings tools
// for the AI. They share the Settings code path with the fast-path rule, so
// the same policy applies: anything security-related is refused and turned
// into a click confirmation.
func SettingsTools(s *Settings) []plugins.Tool {
	return []plugins.Tool{getSettingTool{s}, setSettingTool{s}, listSettingsTool{s}}
}

type getSettingTool struct{ s *Settings }

func (getSettingTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name:        "get_setting",
		Description: "Read one Footman setting, for example input.dictate.key. Secrets are never returned.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"key":{"type":"string","description":"Dotted setting path, such as replies.speak."}},"required":["key"]}`),
		Permission: plugins.Read,
	}
}

func (t getSettingTool) Run(_ context.Context, args json.RawMessage) (plugins.Result, error) {
	var a struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Key == "" {
		return plugins.Result{Text: "key is required", IsError: true}, nil
	}
	reply, st := t.s.Get(a.Key)
	return plugins.Result{Text: reply, IsError: st != Changed}, nil
}

type setSettingTool struct{ s *Settings }

func (setSettingTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name: "set_setting",
		Description: "Change one Footman setting by voice, for example replies.speak = never for quiet mode. " +
			"Security and permission settings are refused and the user is asked to confirm with a click instead; " +
			"tell them so.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"key":{"type":"string","description":"Dotted setting path."},` +
			`"value":{"type":"string","description":"New value as text: on/off, a number, or a word."}},"required":["key","value"]}`),
		Permission: plugins.Act,
	}
}

func (t setSettingTool) Run(_ context.Context, args json.RawMessage) (plugins.Result, error) {
	var a struct{ Key, Value string }
	if err := json.Unmarshal(args, &a); err != nil || a.Key == "" {
		return plugins.Result{Text: "key and value are required", IsError: true}, nil
	}
	reply, st := t.s.Set(a.Key, a.Value)
	return plugins.Result{Text: reply, IsError: st != Changed}, nil
}

type listSettingsTool struct{ s *Settings }

func (listSettingsTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name:        "list_settings",
		Description: "List the settings that can be changed by voice, with their current values.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Permission:  plugins.Read,
	}
}

func (t listSettingsTool) Run(context.Context, json.RawMessage) (plugins.Result, error) {
	l := t.s.List()
	sort.Strings(l)
	return plugins.Result{Text: strings.Join(l, "\n")}, nil
}
