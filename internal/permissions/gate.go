package permissions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/plugins"
)

// Policies, the values of permissions.read|act|destructive|outward.
const (
	PolicyAuto    = "auto"
	PolicyConfirm = "confirm"
	PolicyDeny    = "deny"
)

// DefaultTimeout is how long the gate waits for an answer to a confirmation
// when Gate.Timeout is zero.
const DefaultTimeout = 2 * time.Minute

// Errors the gate refuses a call with. The AI is told the full message, so
// it can explain to the user why nothing happened.
var (
	// ErrDenied: the policy for the level is deny.
	ErrDenied = errors.New("not allowed by the permission settings")
	// ErrNoConfirmer: the call needs confirmation and there is no way to
	// ask for it.
	ErrNoConfirmer = errors.New("needs the user's confirmation, which can't be asked for right now")
	// ErrRefused: the user said no.
	ErrRefused = errors.New("the user refused it")
	// ErrTimeout: nobody answered the confirmation in time.
	ErrTimeout = errors.New("nobody confirmed it in time")
)

// Gate is the permission gate (design §14, §29, ADR-0038). It satisfies
// core.Gate and core.Decider. Set its fields before first use; a Gate is then
// safe for concurrent use.
type Gate struct {
	// Config returns the current config. The policy is read from it on
	// every call, so changes apply at once. Nil means the defaults.
	Config func() *config.Config
	// Confirmer asks the user. Nil means calls that need confirmation are
	// refused.
	Confirmer Confirmer
	// Timeout caps how long a confirmation may take (default
	// DefaultTimeout).
	Timeout time.Duration

	seq atomic.Uint64
}

// Check implements core.Gate.
func (g *Gate) Check(ctx context.Context, spec plugins.ToolSpec, args json.RawMessage, untrusted bool) error {
	_, err := g.Decide(ctx, spec, args, untrusted)
	return err
}

// Decide decides whether a call may run and says how: HowAuto when the
// policy allows it, HowPolicy when the policy (or a missing confirmer)
// refuses it, HowTimeout when nobody answered, or the Confirmer's own How.
// A nil error means the call may run.
func (g *Gate) Decide(ctx context.Context, spec plugins.ToolSpec, args json.RawMessage, untrusted bool) (string, error) {
	if !spec.Permission.Valid() {
		return HowPolicy, fmt.Errorf("%s has invalid permission %v", spec.Name, spec.Permission)
	}
	key := "permissions." + spec.Permission.String()
	policy := g.config().String(key)
	reason := fmt.Sprintf("%s actions need confirmation (%s = %q)", spec.Permission, key, policy)
	// Everything but Read: once untrusted content is in the task, Act
	// actions need confirmation too (ADR-0047, amending ADR-0015).
	risky := spec.Permission != plugins.Read
	if risky && untrusted && policy != PolicyDeny {
		// The untrusted content rule (ADR-0015): whatever the config says.
		policy = PolicyConfirm
		reason = "untrusted content (a web page, message, file or plugin output) is in this task, and it may be trying to make me do this"
	}
	switch policy {
	case PolicyAuto:
		return HowAuto, nil
	case PolicyDeny:
		return HowPolicy, fmt.Errorf("%s is a %s action and %s is \"deny\": %w", spec.Name, spec.Permission, key, ErrDenied)
	case PolicyConfirm:
	default:
		// An unknown value cannot get through config validation; treat
		// it as the safe choice.
		reason = fmt.Sprintf("%s has an unknown value %q", key, policy)
	}
	if g.Confirmer == nil {
		return HowPolicy, fmt.Errorf("%s is a %s action and %w (%s)", spec.Name, spec.Permission, ErrNoConfirmer, reason)
	}
	req := Request{
		ID:     fmt.Sprintf("perm-%d", g.seq.Add(1)),
		Kind:   KindTool,
		Level:  spec.Permission,
		Title:  fmt.Sprintf("Allow %s (%s)?", spec.Name, spec.Permission),
		Detail: Detail(spec.Name, args),
		Reason: reason,
		// Voice may approve Act and Outward actions, never a Destructive
		// one (ADR-0047).
		Question:  Question(spec.Name, args),
		ClickOnly: spec.Permission == plugins.Destructive,
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := g.Confirmer.Confirm(cctx, req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return HowPolicy, ctx.Err()
		case cctx.Err() != nil:
			return HowTimeout, fmt.Errorf("%s: %w", spec.Name, ErrTimeout)
		}
		return HowPolicy, fmt.Errorf("%s needs confirmation and asking failed (%w): %w", spec.Name, err, ErrNoConfirmer)
	}
	how := d.How
	if how == "" {
		how = "confirm"
	}
	if !d.Allowed {
		if how == HowTimeout {
			return how, fmt.Errorf("%s: %w", spec.Name, ErrTimeout)
		}
		return how, fmt.Errorf("%s: %w", spec.Name, ErrRefused)
	}
	return how, nil
}

// Question is the short yes/no question spoken for a call: "Shall I open
// example.com?". It names the site, app or action when the arguments have
// one, and otherwise the tool.
func Question(name string, args json.RawMessage) string {
	var a map[string]any
	_ = json.Unmarshal(args, &a)
	str := func(k string) string {
		s, _ := a[k].(string)
		return strings.TrimSpace(s)
	}
	if list, _ := a["urls"].([]any); len(list) > 0 {
		// Several pages are one question, so one confirmation opens them all.
		var hosts []string
		if u := str("url"); u != "" {
			hosts = append(hosts, spokenHost(u))
		}
		for _, x := range list {
			if u, _ := x.(string); strings.TrimSpace(u) != "" {
				hosts = append(hosts, spokenHost(strings.TrimSpace(u)))
			}
		}
		switch len(hosts) {
		case 0:
		case 1:
			return "Shall I open " + hosts[0] + "?"
		default:
			return "Shall I open " + strings.Join(hosts[:len(hosts)-1], ", ") + " and " + hosts[len(hosts)-1] + "?"
		}
	}
	switch {
	case str("url") != "":
		return "Shall I open " + spokenHost(str("url")) + "?"
	case str("app") != "":
		return "Shall I open " + str("app") + "?"
	case str("command") != "": // a fast-path rule: the user's own words
		return "Shall I " + strings.TrimRight(str("command"), ".!? ") + "?"
	case str("action") != "":
		return "Shall I do this: " + strings.TrimRight(str("action"), ".!? ") + "?"
	}
	return "Shall I go ahead with " + strings.ReplaceAll(name, "_", " ") + "?"
}

// spokenHost is the host of a URL without "www.", or the text itself when it
// does not parse as one.
func spokenHost(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return strings.TrimPrefix(s, "www.")
}

func (g *Gate) config() *config.Config {
	if g.Config != nil {
		if c := g.Config(); c != nil {
			return c
		}
	}
	return defaults
}

var defaults = config.Defaults()

// Detail is what a confirmation shows for a call: the tool and exactly the
// arguments it will be given, indented. It is not redacted: the user must
// see exactly what would be sent or deleted.
func Detail(name string, args json.RawMessage) string {
	if len(bytes.TrimSpace(args)) == 0 {
		return name + " (no arguments)"
	}
	var b bytes.Buffer
	if err := json.Indent(&b, args, "", "  "); err != nil {
		return name + " " + string(args)
	}
	return name + " " + b.String()
}
