package config

import (
	"fmt"
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
	// VoiceNeedsClick: the key must never be changed by voice; the user
	// has to confirm with a physical click (design §17, §29).
	VoiceNeedsClick
)

// neverByVoice lists key prefixes that are never changed by voice even if
// the schema were to mark them voice-settable: permissions, plugin trust,
// remote access, secrets and what the AI may see.
var neverByVoice = []string{"permissions.", "plugins", "web.", "ai.providers.", "ai.budget.", "context.", "memory."}

// VoicePolicy decides whether the key at path may be changed by voice. Only
// keys the schema marks VoiceSettable, that are not security-sensitive,
// secrets or under a never-by-voice prefix, are allowed. The returned reason
// explains a refusal in plain words.
func VoicePolicy(path string) (k Key, v Verdict, reason string) {
	k, ok := Lookup(path)
	if !ok {
		return k, VoiceUnknown, ""
	}
	switch {
	case k.Type == Secret:
		return k, VoiceNeedsClick, "it holds a secret"
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

// matchEnum maps spoken text such as "when voice" onto an enum value like
// "when-voice". Other text is returned unchanged.
func matchEnum(k Key, s string) string {
	for _, e := range k.Enum {
		for _, sep := range []string{"-", "_", " "} {
			if strings.EqualFold(strings.ReplaceAll(e, "-", sep), s) {
				return e
			}
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
