package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/plugins"
)

// FastAction is a command matched locally, without the AI (design §5: target
// under 100 ms). Run does the work and returns an optional reply.
type FastAction func(ctx context.Context, p *Pipeline) (reply string, err error)

// FastRule matches one kind of simple command. Match is given the command
// text and returns the action to run, or false.
type FastRule struct {
	// Name identifies the rule in events and logs.
	Name string
	// Permission is the level of what the rule does (design §29). Every
	// built-in rule is Act.
	Permission plugins.Permission
	// Match recognizes the command.
	Match func(text string) (FastAction, bool)
}

// DefaultSites maps spoken site names to URLs for "open <site>".
var DefaultSites = map[string]string{
	"youtube": "https://www.youtube.com",
	"google":  "https://www.google.com",
	"github":  "https://github.com",
	"gmail":   "https://mail.google.com",
}

// OpenRule matches "open <url, site or app>" (and "launch …"). A known site
// name or anything that looks like a URL opens in the browser; anything else
// is started as an app.
func OpenRule(sites map[string]string) FastRule {
	verbs := [][]string{{"open"}, {"launch"}}
	return FastRule{Name: "open", Permission: plugins.Act, Match: func(text string) (FastAction, bool) {
		tokens, ok := stripPrefix(strings.Fields(text), verbs)
		if !ok || len(tokens) == 0 {
			return nil, false
		}
		target := strings.TrimRight(strings.Join(tokens, " "), ".!?,")
		if dictation.Norm(target) == "" {
			return nil, false
		}
		if u, ok := sites[dictation.Norm(target)]; ok {
			return openURL(u, target), true
		}
		if u, ok := asURL(target); ok {
			return openURL(u, target), true
		}
		return func(ctx context.Context, p *Pipeline) (string, error) {
			if err := p.d.Platform.Launcher().OpenApp(ctx, target); err != nil {
				if errors.Is(err, platform.ErrNotFound) {
					return fmt.Sprintf("I couldn't find an app called %s.", target), nil
				}
				return "", err
			}
			return "Opening " + target + ".", nil
		}, true
	}}
}

func openURL(u, name string) FastAction {
	return func(ctx context.Context, p *Pipeline) (string, error) {
		if err := p.d.Platform.Launcher().OpenURL(ctx, u); err != nil {
			return "", err
		}
		return "Opening " + name + ".", nil
	}
}

// asURL returns target as a URL if it already is one ("https://…") or looks
// like a host name ("youtube.com", "docs.example.org/path").
func asURL(target string) (string, bool) {
	if strings.Contains(target, "://") {
		return target, true
	}
	if strings.ContainsAny(target, " \t") {
		return "", false
	}
	host, _, _ := strings.Cut(target, "/")
	dot := strings.LastIndexByte(host, '.')
	if dot <= 0 || dot == len(host)-1 {
		return "", false
	}
	return "https://" + strings.ToLower(host) + target[len(host):], true
}

// VolumeRule matches "volume up", "volume down", "mute", "unmute" and
// "set volume to 40 percent" (and close variants).
func VolumeRule() FastRule {
	return FastRule{Name: "volume", Permission: plugins.Act, Match: func(text string) (FastAction, bool) {
		w := dictation.NormWords(text)
		for len(w) > 0 && (w[0] == "please" || w[0] == "turn" || w[0] == "set") {
			w = w[1:]
		}
		w = dropWords(w, "the", "to", "percent", "it")
		vol := func(f func(context.Context, platform.VolumeControl) error, reply string) (FastAction, bool) {
			return func(ctx context.Context, p *Pipeline) (string, error) {
				v, ok := p.d.Platform.(platform.VolumeControl)
				if !ok {
					return "", fmt.Errorf("volume: %w", platform.ErrUnsupported)
				}
				if err := f(ctx, v); err != nil {
					return "", err
				}
				return reply, nil
			}, true
		}
		const step = 10
		switch strings.Join(w, " ") {
		case "volume up", "up volume", "louder":
			return vol(func(ctx context.Context, v platform.VolumeControl) error { return v.ChangeVolume(ctx, step) }, "Volume up.")
		case "volume down", "down volume", "quieter":
			return vol(func(ctx context.Context, v platform.VolumeControl) error { return v.ChangeVolume(ctx, -step) }, "Volume down.")
		case "mute", "volume mute", "mute volume":
			return vol(func(ctx context.Context, v platform.VolumeControl) error { return v.SetMute(ctx, true) }, "Muted.")
		case "unmute", "volume unmute", "unmute volume":
			return vol(func(ctx context.Context, v platform.VolumeControl) error { return v.SetMute(ctx, false) }, "Unmuted.")
		}
		if len(w) == 2 && (w[0] == "volume" || w[1] == "volume") {
			n := w[0]
			if w[0] == "volume" {
				n = w[1]
			}
			if pct, err := strconv.Atoi(n); err == nil && pct >= 0 && pct <= 100 {
				return vol(func(ctx context.Context, v platform.VolumeControl) error { return v.SetVolume(ctx, pct) },
					fmt.Sprintf("Volume %d percent.", pct))
			}
		}
		return nil, false
	}}
}

func dropWords(w []string, drop ...string) []string {
	var out []string
outer:
	for _, x := range w {
		for _, d := range drop {
			if x == d {
				continue outer
			}
		}
		out = append(out, x)
	}
	return out
}

// MediaRule matches "play music", "pause", "next track", "previous song" and
// close variants.
func MediaRule() FastRule {
	return FastRule{Name: "media", Permission: plugins.Act, Match: func(text string) (FastAction, bool) {
		w := dropWords(dictation.NormWords(text), "please", "the", "my", "music", "media", "song", "track", "playback")
		var cmd platform.MediaCommand
		var reply string
		switch strings.Join(w, " ") {
		case "play", "resume", "unpause":
			cmd, reply = platform.MediaPlay, "Playing."
		case "pause", "stop":
			cmd, reply = platform.MediaPause, "Paused."
		case "next", "skip", "skip next", "play next":
			cmd, reply = platform.MediaNext, "Next."
		case "previous", "back", "go back", "play previous", "last":
			cmd, reply = platform.MediaPrevious, "Previous."
		default:
			return nil, false
		}
		return func(ctx context.Context, p *Pipeline) (string, error) {
			m, ok := p.d.Platform.(platform.MediaControl)
			if !ok {
				return "", fmt.Errorf("media: %w", platform.ErrUnsupported)
			}
			if err := m.Media(ctx, cmd); err != nil {
				if errors.Is(err, platform.ErrNotFound) {
					return "Nothing is playing.", nil
				}
				return "", err
			}
			return reply, nil
		}, true
	}}
}

// DefaultFastRules returns the built-in fast-path rules. sites are the user's
// own site aliases (spoken name to URL), added to DefaultSites.
func DefaultFastRules(sites map[string]string) []FastRule {
	all := make(map[string]string, len(DefaultSites)+len(sites))
	for k, v := range DefaultSites {
		all[k] = v
	}
	for k, v := range sites {
		all[dictation.Norm(k)] = v
	}
	return []FastRule{OpenRule(all), VolumeRule(), MediaRule()}
}

// fillers are words people start a command with that change nothing, as in
// "and volume down" or "okay, pause the music".
var fillers = map[string]bool{
	"and": true, "so": true, "um": true, "uh": true, "uhm": true, "er": true,
	"okay": true, "ok": true, "oh": true, "also": true, "then": true, "now": true,
}

// stripFillers drops leading filler words, so the fast-path rules still match.
func stripFillers(text string) string {
	tokens := strings.Fields(text)
	for len(tokens) > 1 && fillers[dictation.Norm(tokens[0])] {
		tokens = tokens[1:]
	}
	return strings.Join(tokens, " ")
}

// PersonaSwitcher switches the default persona for PersonaRule. The daemon's
// sets personas.default through Settings and applies it at once.
type PersonaSwitcher interface {
	// Find returns the ID of the persona a spoken name refers to, given as
	// normalized words ("klaus", "lucy"), or false.
	Find(name string) (id string, ok bool)
	// Switch makes id the default persona and returns what to say back,
	// which is spoken in the new persona's voice.
	Switch(ctx context.Context, id string) (reply string, err error)
}

// switchVerbs start a persona switch: "switch to Klaus", "use Lucy", "change
// persona to Pettigrew", "let me talk to Ashford".
var switchVerbs = [][]string{
	{"switch", "persona", "to"}, {"change", "persona", "to"}, {"set", "persona", "to"},
	{"switch", "over", "to"}, {"switch", "back", "to"}, {"switch", "to"}, {"change", "to"}, {"swap", "to"},
	{"let", "me", "talk", "to"}, {"let", "me", "speak", "to"}, {"talk", "to"}, {"speak", "to"},
	{"bring", "in"}, {"use"},
}

// PersonaRule matches "switch to <persona>", "use <persona>" and close
// variants for a persona s knows by that name. Anything else, such as "use
// Google", is not matched and falls through to the next rule or the AI.
func PersonaRule(s PersonaSwitcher) FastRule {
	return FastRule{Name: "persona", Permission: plugins.Act, Match: func(text string) (FastAction, bool) {
		w := dropWords(dictation.NormWords(text), "please", "the", "my")
		rest, ok := stripPrefix(w, switchVerbs)
		if !ok {
			return nil, false
		}
		rest = dropWords(rest, "persona", "voice", "butler", "assistant", "instead", "now")
		if len(rest) == 0 {
			return nil, false
		}
		id, ok := s.Find(strings.Join(rest, " "))
		if !ok {
			return nil, false
		}
		return func(ctx context.Context, _ *Pipeline) (string, error) { return s.Switch(ctx, id) }, true
	}}
}
