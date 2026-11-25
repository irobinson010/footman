package core

import (
	"context"
	"strings"

	"github.com/irobinson010/footman/internal/dictation"
)

// FastAction is a command matched locally, without the AI (design §5: target
// under 100 ms). Run does the work and returns an optional reply.
type FastAction func(ctx context.Context, p *Pipeline) (reply string, err error)

// FastRule matches one kind of simple command. Match is given the command
// text and returns the action to run, or false.
type FastRule struct {
	// Name identifies the rule in events and logs.
	Name string
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
	return FastRule{Name: "open", Match: func(text string) (FastAction, bool) {
		tokens, ok := stripPrefix(strings.Fields(text), verbs)
		if !ok || len(tokens) == 0 {
			return nil, false
		}
		target := strings.TrimRight(strings.Join(tokens, " "), ".!?,")
		if dictation.Norm(target) == "" {
			return nil, false
		}
		if u, ok := sites[dictation.Norm(target)]; ok {
			return openURL(u), true
		}
		if u, ok := asURL(target); ok {
			return openURL(u), true
		}
		return func(ctx context.Context, p *Pipeline) (string, error) {
			return "", p.d.Platform.Launcher().OpenApp(ctx, target)
		}, true
	}}
}

func openURL(u string) FastAction {
	return func(ctx context.Context, p *Pipeline) (string, error) {
		return "", p.d.Platform.Launcher().OpenURL(ctx, u)
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

// DefaultFastRules returns the built-in fast-path rules.
func DefaultFastRules(sites map[string]string) []FastRule {
	if sites == nil {
		sites = DefaultSites
	}
	return []FastRule{OpenRule(sites)}
}
