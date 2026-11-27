package permissions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/permissions"
	"github.com/irobinson010/footman/internal/plugins"
)

// The gate satisfies the core interfaces.
var (
	_ core.Gate    = (*permissions.Gate)(nil)
	_ core.Decider = (*permissions.Gate)(nil)
)

// fakeConfirmer answers every request with a fixed decision (or error) and
// records what it was asked.
type fakeConfirmer struct {
	d    permissions.Decision
	err  error
	wait bool // block until ctx ends

	mu   sync.Mutex
	reqs []permissions.Request
}

func (f *fakeConfirmer) Confirm(ctx context.Context, r permissions.Request) (permissions.Decision, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
	if f.wait {
		<-ctx.Done()
		return permissions.Decision{}, ctx.Err()
	}
	return f.d, f.err
}

func (f *fakeConfirmer) asked() []permissions.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]permissions.Request(nil), f.reqs...)
}

func mustParse(t *testing.T, src string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func policyConfig(t *testing.T, level plugins.Permission, policy string) *config.Config {
	return mustParse(t, fmt.Sprintf("version = 1\n[permissions]\n%s = %q\n", level, policy))
}

// TestGateTable covers level × policy × untrusted × the confirmer's answer.
func TestGateTable(t *testing.T) {
	type answer int
	const (
		yes answer = iota
		no
		none    // no confirmer
		timeout // the confirmer never answers
		broken  // the confirmer fails
	)
	answers := map[answer]string{yes: "yes", no: "no", none: "none", timeout: "timeout", broken: "broken"}
	args := json.RawMessage(`{"path":"/home/me/report.txt"}`)
	for _, level := range []plugins.Permission{plugins.Read, plugins.Act, plugins.Destructive, plugins.Outward} {
		// Everything but Read asks once untrusted content is in the task
		// (ADR-0047 extends ADR-0015 to Act).
		risky := level != plugins.Read
		for _, policy := range []string{"auto", "confirm", "deny"} {
			for _, untrusted := range []bool{false, true} {
				for _, ans := range []answer{yes, no, none, timeout, broken} {
					name := fmt.Sprintf("%s/%s/untrusted=%v/%s", level, policy, untrusted, answers[ans])
					t.Run(name, func(t *testing.T) {
						cfg := policyConfig(t, level, policy)
						g := &permissions.Gate{Config: func() *config.Config { return cfg }, Timeout: 20 * time.Millisecond}
						var fc *fakeConfirmer
						switch ans {
						case yes:
							fc = &fakeConfirmer{d: permissions.Decision{Allowed: true, How: permissions.HowClick}}
						case no:
							fc = &fakeConfirmer{d: permissions.Decision{Allowed: false, How: permissions.HowVoice}}
						case timeout:
							fc = &fakeConfirmer{wait: true}
						case broken:
							fc = &fakeConfirmer{err: errors.New("no notification daemon")}
						}
						if fc != nil {
							g.Confirmer = fc
						}
						spec := plugins.ToolSpec{Name: "delete_file", Permission: level}
						how, err := g.Decide(context.Background(), spec, args, untrusted)

						asks := policy == "confirm" || (policy == "auto" && untrusted && risky)
						var wantHow string
						var wantErr error
						switch {
						case policy == "deny":
							wantHow, wantErr = permissions.HowPolicy, permissions.ErrDenied
						case !asks:
							wantHow = permissions.HowAuto
						case ans == yes:
							wantHow = permissions.HowClick
						case ans == no:
							wantHow, wantErr = permissions.HowVoice, permissions.ErrRefused
						case ans == none || ans == broken:
							wantHow, wantErr = permissions.HowPolicy, permissions.ErrNoConfirmer
						case ans == timeout:
							wantHow, wantErr = permissions.HowTimeout, permissions.ErrTimeout
						}
						if how != wantHow {
							t.Errorf("how = %q, want %q", how, wantHow)
						}
						if !errors.Is(err, wantErr) || (wantErr == nil) != (err == nil) {
							t.Errorf("err = %v, want %v", err, wantErr)
						}
						if err != nil && !strings.Contains(err.Error(), "delete_file") {
							t.Errorf("error %q doesn't name the tool, so the AI can't explain it", err)
						}
						if cerr := g.Check(context.Background(), spec, args, untrusted); (cerr == nil) != (err == nil) {
							t.Errorf("Check = %v, Decide = %v", cerr, err)
						}
						if fc == nil {
							return
						}
						reqs := fc.asked()
						if !asks {
							if len(reqs) != 0 {
								t.Errorf("asked %d times, want none", len(reqs))
							}
							return
						}
						if len(reqs) == 0 {
							t.Fatal("the confirmer was not asked")
						}
						r := reqs[0]
						// Only a Destructive action is click-only (ADR-0047).
						if r.Kind != permissions.KindTool || r.Level != level || r.ClickOnly != (level == plugins.Destructive) || r.ID == "" || r.Title == "" || r.Question == "" {
							t.Errorf("request = %+v", r)
						}
						if !strings.Contains(r.Detail, "/home/me/report.txt") {
							t.Errorf("detail %q doesn't show exactly what is affected", r.Detail)
						}
						if untrusted && risky && !strings.Contains(r.Reason, "untrusted") {
							t.Errorf("reason %q doesn't mention the untrusted content", r.Reason)
						}
					})
				}
			}
		}
	}
}

// The default policy: read and act run, destructive and outward ask.
func TestGateDefaults(t *testing.T) {
	g := &permissions.Gate{} // no config: the defaults
	for level, want := range map[plugins.Permission]error{
		plugins.Read: nil, plugins.Act: nil, plugins.Destructive: permissions.ErrNoConfirmer, plugins.Outward: permissions.ErrNoConfirmer,
	} {
		err := g.Check(context.Background(), plugins.ToolSpec{Name: "x", Permission: level}, nil, false)
		if !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("%s: err = %v, want %v", level, err, want)
		}
	}
	if err := g.Check(context.Background(), plugins.ToolSpec{Name: "x"}, nil, false); err == nil {
		t.Error("a tool without a valid permission was allowed")
	}
}

// The policy is read on every call, so a config change applies at once.
func TestGateReadsConfigLive(t *testing.T) {
	cur := policyConfig(t, plugins.Act, "auto")
	g := &permissions.Gate{Config: func() *config.Config { return cur }}
	spec := plugins.ToolSpec{Name: "open_app", Permission: plugins.Act}
	if err := g.Check(context.Background(), spec, nil, false); err != nil {
		t.Fatal(err)
	}
	cur = policyConfig(t, plugins.Act, "deny")
	if err := g.Check(context.Background(), spec, nil, false); !errors.Is(err, permissions.ErrDenied) {
		t.Errorf("after act = deny: err = %v", err)
	}
}

// A cancelled task cancels the question and refuses the call.
func TestGateCancelled(t *testing.T) {
	fc := &fakeConfirmer{wait: true}
	g := &permissions.Gate{Confirmer: fc}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Decide(ctx, plugins.ToolSpec{Name: "rm", Permission: plugins.Destructive}, nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDetail(t *testing.T) {
	got := permissions.Detail("send_email", json.RawMessage(`{"to":"bob@example.com","body":"hi"}`))
	want := "send_email {\n  \"to\": \"bob@example.com\",\n  \"body\": \"hi\"\n}"
	if got != want {
		t.Errorf("Detail = %q, want %q", got, want)
	}
	if got := permissions.Detail("ping", nil); got != "ping (no arguments)" {
		t.Errorf("Detail without args = %q", got)
	}
}
