package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/irobinson010/footman/internal/api"
)

func init() {
	register(command{name: "actions", summary: "show the action log: what footman did, for whom, and how it was confirmed", run: cmdActions})
}

func cmdActions(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("actions", "[-n N] [--json]", stderr)
	n := fs.Int("n", api.DefaultActionsLimit, fmt.Sprintf("how many of the latest entries to show (at most %d)", api.MaxActionsLimit))
	asJSON := fs.Bool("json", false, "print the raw actions.list result")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 || *n < 1 {
		fs.Usage()
		return 2
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()
	var raw json.RawMessage
	if err := c.Call(ctx, "actions.list", api.ActionsParams{Limit: *n}, &raw); err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return 0
	}
	var res api.ActionsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return fail(stderr, err)
	}
	printActions(stdout, res.Actions)
	return 0
}

// printActions writes entries oldest first, so the latest is at the bottom
// like a log.
func printActions(w io.Writer, actions []api.ActionInfo) {
	if len(actions) == 0 {
		_, _ = fmt.Fprintln(w, "The action log is empty.")
		return
	}
	for i := len(actions) - 1; i >= 0; i-- {
		a := actions[i]
		what := a.Action + " " + a.Tool
		if a.Args != "" {
			what += " " + oneLine(a.Args)
		}
		_, _ = fmt.Fprintf(w, "%s  %-5s  %s\n", a.At.Local().Format("2006-01-02 15:04:05"), a.Actor, what)
		if a.Result != "" {
			_, _ = fmt.Fprintf(w, "    result: %s\n", oneLine(a.Result))
		}
		if a.Confirmation != "" {
			_, _ = fmt.Fprintf(w, "    confirmation: %s\n", a.Confirmation)
		}
		if a.Reason != "" {
			_, _ = fmt.Fprintf(w, "    because: %s\n", oneLine(a.Reason))
		}
	}
}

// oneLine folds whitespace so an entry stays on its lines, and caps it.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 200
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}
