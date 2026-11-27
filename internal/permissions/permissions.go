package permissions

import (
	"context"

	"github.com/irobinson010/footman/internal/plugins"
)

// Request is one question put to the user before an action runs: a tool call
// the gate wants confirmed, or a setting change that needs a click. A
// Confirmer shows it (a notification, a spoken prompt) and returns a Decision.
type Request struct {
	ID     string
	Kind   string // "tool" | "setting"
	Level  plugins.Permission
	Title  string // short, for a notification
	Detail string // exactly what will be sent, deleted or changed
	Reason string // why confirmation is needed
	// Question is the short yes/no question Footman speaks when the reply
	// is spoken, such as "Shall I open example.com?" (ADR-0047). Empty
	// means the persona's generic confirm line.
	Question  string
	ClickOnly bool // security-sensitive: a voice "yes" never counts
}

// Request kinds.
const (
	KindTool    = "tool"
	KindSetting = "setting"
)

// Decision is the answer to a Request. How records how it was reached, for
// the action log.
type Decision struct {
	Allowed bool
	How     string // How: "auto" | "click" | "voice" | "policy" | "timeout"
}

// Values of Decision.How.
const (
	HowAuto    = "auto"    // the policy allowed it without asking
	HowClick   = "click"   // the user clicked a confirmation
	HowVoice   = "voice"   // the user answered by voice
	HowPolicy  = "policy"  // the policy decided without asking (a deny, or no confirmer)
	HowTimeout = "timeout" // nobody answered in time; refused
)

// Confirmer asks the user to allow or refuse a Request. It blocks until the
// user answers, ctx ends, or its own timeout passes. A refusal is a Decision
// with Allowed false, not an error; an error means the question could not be
// asked at all, and the caller treats it as a refusal.
type Confirmer interface {
	Confirm(ctx context.Context, r Request) (Decision, error)
}
