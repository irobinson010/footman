package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/core"
)

func init() {
	register(command{name: "status", summary: "show whether the daemon is running and what it is doing", run: cmdStatus})
	register(command{name: "say", summary: "send a command (or dictation) to footman as text", run: cmdSay})
	register(command{name: "stop", summary: "stop everything: cancel jobs and speech, mute the microphone", run: cmdStop})
	register(command{name: "events", summary: "stream live events from the daemon", run: cmdEvents})
}

// clientFlags returns a flag set with the options every client command has.
func clientFlags(name, use string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("footman "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultSocket(), "daemon socket (or set $"+socketEnv+")")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: footman %s %s\n\nOptions:\n", name, use)
		fs.PrintDefaults()
	}
	return fs, socket
}

// parseFlags parses args, returning an exit code and false when the command
// should stop (help asked for, or bad usage).
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

// dial connects to the daemon, explaining the usual failure.
func dial(ctx context.Context, socket string, stderr io.Writer) *api.Client {
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := api.Dial(dctx, socket)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "footman: the daemon is not running (nothing answers on %s).\nStart it with `footman daemon`.\n", socket)
		return nil
	}
	return c
}

func cmdStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("status", "[--json]", stderr)
	asJSON := fs.Bool("json", false, "print the raw status.get result")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()
	var raw json.RawMessage
	if err := c.Call(ctx, "status.get", nil, &raw); err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return 0
	}
	var s api.StatusResult
	if err := json.Unmarshal(raw, &s); err != nil {
		return fail(stderr, err)
	}
	mic := micText(s)
	jobs := "none"
	if len(s.Jobs) > 0 {
		jobs = strings.Join(s.Jobs, ", ")
	}
	uptime := time.Duration(s.UptimeSeconds * float64(time.Second)).Round(time.Second)
	_, _ = fmt.Fprintf(stdout, "footman %s is running (pid %d, up %s, API %s)\n", s.Version, s.PID, uptime, s.API)
	_, _ = fmt.Fprintf(stdout, "  state:       %s\n", s.State)
	_, _ = fmt.Fprintf(stdout, "  microphone:  %s\n", mic)
	_, _ = fmt.Fprintf(stdout, "  jobs:        %s\n", jobs)
	var parts []string
	for _, a := range []string{"platform", "stt", "vad", "wake", "tts", "llm", "worker"} {
		name := s.Implementations[a]
		if name == "" {
			name = "none"
		}
		parts = append(parts, a+"="+name)
	}
	_, _ = fmt.Fprintf(stdout, "  engines:     %s\n", strings.Join(parts, " "))
	_, _ = fmt.Fprintf(stdout, "  models:      %s\n", modelsLine(s.Models, time.Now()))
	if s.DevFakes {
		_, _ = fmt.Fprintf(stdout, "  WARNING:     started with --dev-fakes (development only)\n")
	}
	_, _ = fmt.Fprintf(stdout, "  socket:      %s\n", s.Socket)
	_, _ = fmt.Fprintf(stdout, "  config:      %s\n", s.Config)
	return 0
}

// micText describes the microphone loop for footman status. A stopped loop is
// shown as stopped with the error that stopped it. A daemon too old to report
// the loop's state gets the muted flag alone.
func micText(s api.StatusResult) string {
	switch s.Microphone.State {
	case core.MicStopped:
		return "stopped: " + s.Microphone.Error
	case core.MicOff:
		return "not in use"
	case core.MicOn:
		return "on"
	case core.MicMuted:
		return "muted (after a stop)"
	}
	if s.Muted {
		return "muted (after a stop)"
	}
	return "on"
}

func cmdSay(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("say", "[--dictate] <text>...", stderr)
	dictate := fs.Bool("dictate", false, "type the text at the cursor as dictation instead of running it as a command")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		fs.Usage()
		return 2
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()
	p := api.SayParams{Text: text}
	if *dictate {
		p.As = "dictate"
	}
	if err := c.Call(ctx, "say.text", p, nil); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func cmdStop(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("stop", "", stderr)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()
	var res api.StopResult
	if err := c.Call(ctx, "system.stop_everything", nil, &res); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "Stopped everything: cancelled %s and %s. The microphone is muted.\n",
		plural(len(res.Jobs), "job"), plural(res.Turns, "request"))
	return 0
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// topicList is a repeatable --topic flag.
type topicList []string

func (t *topicList) String() string { return strings.Join(*t, ",") }

func (t *topicList) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*t = append(*t, s)
		}
	}
	return nil
}

func cmdEvents(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("events", "[--topic <topic>]... [--json]", stderr)
	var topics topicList
	fs.Var(&topics, "topic", "only this topic, a prefix like pipeline.*, or a comma-separated list (repeatable; default all)")
	asJSON := fs.Bool("json", false, "print each event as a JSON object")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()
	if err := c.Call(ctx, "events.subscribe", api.SubscribeParams{Topics: topics}, nil); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintln(stderr, "footman: streaming events; press Ctrl-C to stop")
	for {
		select {
		case <-ctx.Done():
			return 0
		case n, ok := <-c.Notifications():
			if !ok {
				_, _ = fmt.Fprintln(stderr, "footman: the daemon closed the connection")
				return 1
			}
			if n.Method != api.EventMethod {
				continue
			}
			if *asJSON {
				_, _ = fmt.Fprintln(stdout, string(n.Params))
				continue
			}
			var ev api.Event
			if err := json.Unmarshal(n.Params, &ev); err != nil {
				continue
			}
			_, _ = fmt.Fprintf(stdout, "%s %-20s %s\n", ev.At.Local().Format("15:04:05.000"), ev.Topic, ev.Data)
		}
	}
}

// modelsLine summarizes which speech models are in memory, such as
// "stt loaded, vad loaded, tts unloaded 4m ago (unload after 10m0s idle)".
func modelsLine(m api.ModelsStatus, now time.Time) string {
	if len(m.Models) == 0 {
		return "none"
	}
	var parts []string
	for _, x := range m.Models {
		switch {
		case x.Loaded && x.InUse > 0:
			parts = append(parts, x.Name+" loaded (in use)")
		case x.Loaded:
			parts = append(parts, x.Name+" loaded")
		default:
			parts = append(parts, fmt.Sprintf("%s unloaded %s ago", x.Name, now.Sub(x.Since).Round(time.Second)))
		}
	}
	when := "never unload"
	if d, err := time.ParseDuration(m.UnloadAfter); err == nil && d > 0 {
		when = "unload after " + d.String() + " idle"
	}
	return strings.Join(parts, ", ") + " (" + when + ")"
}
