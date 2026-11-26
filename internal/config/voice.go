package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Verdict says whether a key may be changed by voice.
type Verdict int

// Verdicts for VoicePolicy.
const (
	// VoiceUnknown: no such key (or a free-form plugin key).
	VoiceUnknown Verdict = iota
	// VoiceAllowed: the key may be changed by voice.
	VoiceAllowed
	// VoiceNeedsClick: the change may not be made by voice alone; the
	// user has to confirm it with a physical click (design §17, §29).
	VoiceNeedsClick
	// VoiceNever: the key is never set by voice, not even with a click
	// (secrets: speaking one aloud would already leak it).
	VoiceNever
)

// neverByVoice lists key prefixes that are never changed by voice even if
// the schema were to mark them voice-settable: permissions, plugin trust,
// remote access, secrets and what the AI may see.
var neverByVoice = []string{"permissions.", "plugins", "web.", "ai.providers.", "ai.budget.", "context.", "memory."}

// VoicePolicy decides whether the key at path may be changed by voice,
// whatever the new value. Only keys the schema marks VoiceSettable, that are
// not security-sensitive, secrets or under a never-by-voice prefix, are
// allowed; secrets are VoiceNever. The returned reason explains a refusal in
// plain words. VoiceChange refines this for a particular change.
func VoicePolicy(path string) (k Key, v Verdict, reason string) {
	k, ok := Lookup(path)
	if !ok {
		return k, VoiceUnknown, ""
	}
	switch {
	case k.Type == Secret:
		return k, VoiceNever, "it holds a secret"
	case k.Sensitive:
		return k, VoiceNeedsClick, "it affects security or permissions"
	}
	for _, p := range neverByVoice {
		if k.Path == strings.TrimSuffix(p, ".") || strings.HasPrefix(k.Path, p) {
			return k, VoiceNeedsClick, "it affects security, permissions or access"
		}
	}
	if !k.VoiceSettable {
		return k, VoiceNeedsClick, "it can't be changed by voice"
	}
	return k, VoiceAllowed, ""
}

// VoiceChange decides whether the key at path may be changed by voice from
// old to new (both as Config.Get returns them, or as ParseValue makes them).
// It is VoicePolicy made direction-aware (design §29, ADR-0039): a change
// that tightens security, or changes nothing, is allowed by voice at once,
// while one that loosens it still needs a click. Secrets stay VoiceNever.
func VoiceChange(path string, old, new any) (k Key, v Verdict, reason string) {
	k, v, reason = VoicePolicy(path)
	if v != VoiceNeedsClick {
		return k, v, reason
	}
	if fmt.Sprint(old) == fmt.Sprint(new) || Tightens(path, old, new) {
		return k, VoiceAllowed, ""
	}
	return k, v, reason
}

// Tightens reports whether changing path from old to new makes Footman
// stricter: a permission level towards deny, the action log on, memory off,
// fewer things the AI may see, or AI requests kept closer to the machine.
// Keys with no such direction never tighten. Equal values do not tighten.
func Tightens(path string, old, new any) bool {
	switch path {
	case "permissions.read", "permissions.act", "permissions.destructive", "permissions.outward":
		return rankUp(permLevels, old, new)
	case "ai.mode":
		return rankUp([]string{"cloud", "local-first", "offline"}, old, new)
	case "permissions.action_log":
		return old != true && new == true
	case "memory.enabled":
		return old != false && new == false
	case "context.local", "context.cloud":
		o, ok1 := asList(old)
		n, ok2 := asList(new)
		if !ok1 || !ok2 {
			return false
		}
		o, n = expandContext(o), expandContext(n)
		if len(n) >= len(o) {
			return false
		}
		for _, x := range n {
			if !slices.Contains(o, x) {
				return false
			}
		}
		return true
	}
	return false
}

// rankUp reports whether new comes later than old in order (from loosest
// to strictest). Unknown values never tighten.
func rankUp(order []string, old, new any) bool {
	o, ok1 := old.(string)
	n, ok2 := new.(string)
	if !ok1 || !ok2 {
		return false
	}
	i, j := slices.Index(order, o), slices.Index(order, n)
	return i >= 0 && j > i
}

func asList(v any) ([]string, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case []string:
		return t, true
	}
	return nil, false
}

// expandContext replaces "all" with every context item, deduplicated.
func expandContext(items []string) []string {
	var out []string
	for _, x := range items {
		add := []string{x}
		if x == "all" {
			add = slices.DeleteFunc(slices.Clone(contextItems), func(s string) bool { return s == "all" })
		}
		for _, a := range add {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

// ParseValue converts spoken text into a value of the key's type, validated
// against the schema. Errors are phrased to be read back to the user.
func ParseValue(path, text string) (any, error) {
	k, ok := Lookup(path)
	if !ok {
		return nil, fmt.Errorf("%s is not a setting", path)
	}
	text = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(text), ".!?"))
	switch k.Type {
	case Bool:
		switch strings.ToLower(text) {
		case "on", "true", "yes", "enable", "enabled":
			return true, nil
		case "off", "false", "no", "disable", "disabled":
			return false, nil
		}
		return nil, fmt.Errorf("%s needs on or off, not %q", path, text)
	case Int:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s needs a whole number, not %q", path, text)
		}
		return coerceMsg(k, n)
	case Float:
		x, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("%s needs a number, not %q", path, text)
		}
		return coerceMsg(k, x)
	case StringList:
		var list []string
		for _, s := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' }) {
			for _, part := range strings.Split(s, " and ") {
				if part = strings.TrimSpace(part); part != "" {
					list = append(list, matchEnum(k, part))
				}
			}
		}
		return coerceMsg(k, list)
	case String:
		return coerceMsg(k, matchEnum(k, text))
	}
	return nil, fmt.Errorf("%s can't be set by voice", path)
}

// matchEnum maps spoken text such as "when voice" or "window title" onto an
// enum value like "when-voice" or "window_title". Other text is returned
// unchanged.
func matchEnum(k Key, s string) string {
	words := strings.NewReplacer("-", " ", "_", " ")
	for _, e := range k.Enum {
		if strings.EqualFold(words.Replace(e), words.Replace(s)) {
			return e
		}
	}
	return s
}

func coerceMsg(k Key, v any) (any, error) {
	out, err := coerce(k, v)
	if err != nil {
		return nil, fmt.Errorf("%s %w", k.Path, err)
	}
	return out, nil
}
