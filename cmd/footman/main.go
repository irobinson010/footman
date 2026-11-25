// Command footman is the single footman binary: the command line client and
// the daemon (`footman daemon`).
//
// Subcommands live in a table. Each one is declared in its own file
// (cmd_daemon.go, cmd_client.go, ...) and added with register from that
// file's init function, so new subcommands don't touch this file.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// command is one subcommand.
type command struct {
	// name is what the user types: `footman <name>`.
	name string
	// summary is the one-line description shown in the help.
	summary string
	// run runs the command with the arguments after its name and returns
	// the exit code: 0 for success, 1 for failure, 2 for bad usage. ctx is
	// cancelled on SIGINT or SIGTERM.
	run func(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

var commands = map[string]command{}

// register adds a subcommand. Call it from an init function. Registering a
// name twice panics.
func register(c command) {
	if c.name == "" || c.run == nil {
		panic("footman: command needs a name and a run function")
	}
	if _, dup := commands[c.name]; dup {
		panic("footman: command " + c.name + " registered twice")
	}
	commands[c.name] = c
}

func init() {
	register(command{name: "version", summary: "print the version", run: func(_ context.Context, _ []string, stdout, _ io.Writer) int {
		_, _ = fmt.Fprintln(stdout, "footman", version)
		return 0
	}})
	register(command{name: "help", summary: "show this help", run: func(_ context.Context, _ []string, stdout, _ io.Writer) int {
		_, _ = fmt.Fprint(stdout, usage())
		return 0
	}})
}

func usage() string {
	names := make([]string, 0, len(commands))
	width := 0
	for n := range commands {
		names = append(names, n)
		width = max(width, len(n))
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("footman - voice dictation and assistant\n\nUsage:\n  footman <command> [arguments]\n\nCommands:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, n, commands[n].summary)
	}
	b.WriteString("\nRun `footman <command> -h` for a command's options.\n")
	return b.String()
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage())
		return 2
	}
	name := args[0]
	switch name {
	case "--version":
		name = "version"
	case "--help", "-h":
		name = "help"
	}
	c, ok := commands[name]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "footman: unknown command %q\n\n%s", args[0], usage())
		return 2
	}
	return c.run(ctx, args[1:], stdout, stderr)
}
