package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/irobinson010/footman/internal/api"
)

func init() {
	register(command{name: "confirm", summary: "list pending confirmations, or allow or deny one by ID", run: cmdConfirm})
}

func cmdConfirm(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("confirm", "[--json] | allow <id> | deny <id>", stderr)
	asJSON := fs.Bool("json", false, "print the raw confirm.list result")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	var answer *bool
	switch {
	case len(rest) == 0:
	case len(rest) == 2 && (rest[0] == "allow" || rest[0] == "deny") && !*asJSON:
		allow := rest[0] == "allow"
		answer = &allow
	default:
		fs.Usage()
		return 2
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()

	if answer != nil {
		// Typing this at the keyboard is a physical confirmation, like a
		// click on the notification (ADR-0039).
		var res api.ConfirmAnswerResult
		if err := c.Call(ctx, "confirm.answer", api.ConfirmAnswerParams{ID: rest[1], Allow: answer}, &res); err != nil {
			return fail(stderr, err)
		}
		verb := "Denied"
		if res.Allowed {
			verb = "Allowed"
		}
		_, _ = fmt.Fprintf(stdout, "%s %s.\n", verb, res.ID)
		return 0
	}

	var raw json.RawMessage
	if err := c.Call(ctx, "confirm.list", nil, &raw); err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return 0
	}
	var res api.ConfirmListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return fail(stderr, err)
	}
	if len(res.Requests) == 0 {
		_, _ = fmt.Fprintln(stdout, "Nothing is waiting for confirmation.")
		return 0
	}
	for _, p := range res.Requests {
		left := time.Until(p.Expires).Round(time.Second)
		if left < 0 {
			left = 0
		}
		_, _ = fmt.Fprintf(stdout, "%s  %s  (%s, expires in %s)\n", p.ID, p.Title, p.Kind, left)
		_, _ = fmt.Fprintf(stdout, "    %s\n", oneLine(p.Detail))
		if p.Reason != "" {
			_, _ = fmt.Fprintf(stdout, "    why: %s\n", oneLine(p.Reason))
		}
	}
	_, _ = fmt.Fprintln(stdout, "Answer with: footman confirm allow <id> | footman confirm deny <id>")
	return 0
}
