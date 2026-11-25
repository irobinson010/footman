package config

import (
	"sort"
	"strings"
)

// Type is the value type of a config key.
type Type string

// Config value types.
const (
	String     Type = "string"
	Bool       Type = "bool"
	Int        Type = "int"
	Float      Type = "float"
	StringList Type = "string-list"
	// Secret is a string that must be a "keychain:<name>" reference.
	Secret Type = "secret"
)

// Reload says when a changed value takes effect.
type Reload string

// Reload modes.
const (
	Live    Reload = "live"
	Restart Reload = "restart"
)

// Key describes one config key. The schema is plain data so the web UI form,
// voice settings and generated docs can all read it.
type Key struct {
	// Path is the dotted key path. A "*" segment stands for a user-chosen
	// name, for example "ai.providers.*.model".
	Path        string   `json:"path"`
	Type        Type     `json:"type"`
	Default     any      `json:"default"` // nil for wildcard keys
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`   // allowed values (or list items)
	Format      string   `json:"format,omitempty"` // "duration", "timerange" or "hostport"
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	// VoiceSettable: the user may change this by voice.
	VoiceSettable bool   `json:"voice_settable"`
	Reload        Reload `json:"reload"`
	// Sensitive: loosening this needs a physical click (design §29).
	Sensitive bool `json:"security_sensitive"`
}

func f(v float64) *float64 { return &v }

var (
	onOff      = []string{"off", "simple", "smart"}
	permLevels = []string{"auto", "confirm", "deny"}
)

// schema is the single definition of every key. Order is documentation order.
var schema = []Key{
	{Path: "version", Type: Int, Default: int64(1), Description: "Config format version, used for migrations.", Reload: Restart, Min: f(1)},

	{Path: "user.name", Type: String, Default: "", Description: "What the assistant calls you.", VoiceSettable: true, Reload: Live},
	{Path: "user.language", Type: String, Default: "en", Description: "Language code for speech and replies.", VoiceSettable: true, Reload: Live},

	{Path: "personas.default", Type: String, Default: "ashford", Description: "Persona used when none is addressed by name.", VoiceSettable: true, Reload: Live},
	{Path: "personas.listening", Type: StringList, Default: []string{"ashford"}, Description: "Personas that listen at once; more than one means several personas.", VoiceSettable: true, Reload: Live},
	{Path: "personas.overrides.*.title", Type: String, Description: "How this persona addresses you.", VoiceSettable: true, Reload: Live},
	{Path: "personas.overrides.*.quips", Type: String, Description: "How often this persona quips.", VoiceSettable: true, Reload: Live},

	{Path: "input.dictate.key", Type: String, Default: "Super+Space", Description: "Hotkey for dictation.", Reload: Restart},
	{Path: "input.dictate.mode", Type: String, Default: "hold", Enum: []string{"hold", "toggle"}, Description: "Dictation hotkey behaviour.", Reload: Restart},
	{Path: "input.command.key", Type: String, Default: "Super+Shift+Space", Description: "Hotkey for spoken commands.", Reload: Restart},
	{Path: "input.command.mode", Type: String, Default: "hold", Enum: []string{"hold", "toggle"}, Description: "Command hotkey behaviour.", Reload: Restart},
	{Path: "input.wake_word", Type: Bool, Default: true, Description: "Listen for the wake word.", VoiceSettable: true, Reload: Live},
	{Path: "input.microphone", Type: String, Default: "default", Description: "Microphone device name, or \"default\".", Reload: Live},

	{Path: "dictation.cleanup", Type: String, Default: "simple", Enum: onOff, Description: "Transcript cleanup level.", VoiceSettable: true, Reload: Live},
	{Path: "dictation.commands", Type: String, Default: "simple", Enum: onOff, Description: "Spoken editing commands such as \"new line\".", VoiceSettable: true, Reload: Live},
	{Path: "dictation.insert", Type: String, Default: "auto", Enum: []string{"auto", "type", "paste"}, Description: "How text is inserted into the focused app.", VoiceSettable: true, Reload: Live},

	{Path: "replies.speak", Type: String, Default: "when-voice", Enum: []string{"always", "when-voice", "never"}, Description: "When replies are spoken aloud.", VoiceSettable: true, Reload: Live},
	{Path: "replies.quiet_hours", Type: String, Default: "", Format: "timerange", Description: "Local time range with no spoken replies, like 22:00-07:00. Empty means none.", VoiceSettable: true, Reload: Live},

	{Path: "speech.stt.engine", Type: String, Default: "whisper-local", Description: "Speech-to-text engine.", Reload: Restart},
	{Path: "speech.stt.model", Type: String, Default: "auto", Description: "Speech-to-text model, or \"auto\".", Reload: Restart},
	{Path: "speech.stt.accelerator", Type: String, Default: "auto", Description: "Accelerator for speech-to-text (auto, cpu, gpu, ...).", Reload: Restart},
	{Path: "speech.tts.engine", Type: String, Default: "persona", Description: "Text-to-speech engine; \"persona\" uses the persona's voice.", Reload: Live},
	{Path: "speech.unload_after", Type: String, Default: "10m", Format: "duration", Description: "Unload speech models after this much idle time.", Reload: Live},

	{Path: "ai.mode", Type: String, Default: "cloud", Enum: []string{"cloud", "local-first", "offline"}, Description: "Where AI requests may go.", VoiceSettable: true, Reload: Live, Sensitive: true},
	{Path: "ai.provider", Type: String, Default: "claude", Description: "Name of the provider (under ai.providers) to use.", VoiceSettable: true, Reload: Live},
	{Path: "ai.providers.*.api_key", Type: Secret, Description: "Keychain reference such as keychain:claude. Never the key itself.", Reload: Live},
	{Path: "ai.providers.*.model", Type: String, Description: "Model name.", Reload: Live},
	{Path: "ai.providers.*.url", Type: String, Description: "Base URL for self-hosted providers.", Reload: Live},
	{Path: "ai.budget.*.monthly_usd", Type: Float, Description: "Monthly spend limit in US dollars.", Min: f(0), Reload: Live},
	{Path: "ai.budget.*.warn_at", Type: Float, Description: "Warn when this fraction of the budget is used.", Min: f(0), Max: f(1), Reload: Live},

	{Path: "context.local", Type: StringList, Default: []string{}, Enum: contextItems, Description: "What a local AI may see besides your words.", Reload: Live, Sensitive: true},
	{Path: "context.cloud", Type: StringList, Default: []string{}, Enum: contextItems, Description: "What a cloud AI may see besides your words.", Reload: Live, Sensitive: true},

	{Path: "memory.enabled", Type: Bool, Default: false, Description: "Let the assistant remember things between sessions.", VoiceSettable: true, Reload: Live, Sensitive: true},

	{Path: "history.transcripts", Type: Bool, Default: true, Description: "Keep transcripts of what you said.", VoiceSettable: true, Reload: Live},
	{Path: "history.retention_days", Type: Int, Default: int64(30), Min: f(0), Description: "Days to keep transcripts; 0 keeps none.", VoiceSettable: true, Reload: Live},

	{Path: "permissions.read", Type: String, Default: "auto", Enum: permLevels, Description: "Policy for read-only actions.", VoiceSettable: true, Reload: Live, Sensitive: true},
	{Path: "permissions.act", Type: String, Default: "auto", Enum: permLevels, Description: "Policy for actions that change things.", VoiceSettable: true, Reload: Live, Sensitive: true},
	{Path: "permissions.destructive", Type: String, Default: "confirm", Enum: permLevels, Description: "Policy for destructive actions.", VoiceSettable: true, Reload: Live, Sensitive: true},
	{Path: "permissions.outward", Type: String, Default: "confirm", Enum: permLevels, Description: "Policy for actions that send things outside.", VoiceSettable: true, Reload: Live, Sensitive: true},
	{Path: "permissions.action_log", Type: Bool, Default: true, Description: "Record every action in the action log.", Reload: Live, Sensitive: true},
	{Path: "permissions.action_log_retention_days", Type: Int, Default: int64(90), Min: f(0), Description: "Days to keep action log entries.", Reload: Live},

	{Path: "jobs.max_concurrent", Type: Int, Default: int64(2), Min: f(1), Description: "Background jobs that may run at once.", VoiceSettable: true, Reload: Live},
	{Path: "jobs.worker", Type: String, Default: "claude-code", Enum: []string{"builtin", "claude-code", "codex"}, Description: "Worker that runs background jobs.", Reload: Live},
	{Path: "jobs.code_edits.new_branch", Type: Bool, Default: true, Description: "Do code edits on a new git branch.", Reload: Live},
	{Path: "jobs.code_edits.show_diff", Type: Bool, Default: true, Description: "Show the diff after a code edit.", Reload: Live},

	{Path: "coaching.enabled", Type: Bool, Default: false, Description: "Offer coaching tips on how you dictate.", VoiceSettable: true, Reload: Live},

	{Path: "web.listen", Type: String, Default: "127.0.0.1:7878", Format: "hostport", Description: "Address of the local web UI and API.", Reload: Restart, Sensitive: true},
}

var contextItems = []string{"window_title", "clipboard", "selection", "all"}

// Schema returns every known key, in documentation order. The result is a
// copy and safe to modify. Plugin tables ([plugins.<id>]) are free-form and
// not listed; see PluginsPrefix.
func Schema() []Key {
	out := make([]Key, len(schema))
	copy(out, schema)
	return out
}

// PluginsPrefix is the table that holds free-form per-plugin settings.
const PluginsPrefix = "plugins"

// Lookup finds the schema entry for a concrete dotted path, matching "*"
// segments. It returns false for unknown paths and for plugin settings.
func Lookup(path string) (Key, bool) {
	parts := strings.Split(path, ".")
	for _, k := range schema {
		if matchPath(strings.Split(k.Path, "."), parts) {
			return k, true
		}
	}
	return Key{}, false
}

func matchPath(pattern, parts []string) bool {
	if len(pattern) != len(parts) {
		return false
	}
	for i, p := range pattern {
		if p != "*" && p != parts[i] {
			return false
		}
	}
	return true
}

func isPlugin(path string) bool {
	return path == PluginsPrefix || strings.HasPrefix(path, PluginsPrefix+".")
}

// knownTables lists top-level tables, used for error hints.
func knownTables() []string {
	seen := map[string]bool{}
	for _, k := range schema {
		if i := strings.Index(k.Path, "."); i > 0 {
			seen[k.Path[:i]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
