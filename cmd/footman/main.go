// Command footman is the single footman binary: the command line client and
// the daemon (`footman daemon`, added later).
package main

import (
	"fmt"
	"io"
	"os"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `footman - voice dictation and assistant

Usage:
  footman <command>

Commands:
  version   print the version
  help      show this help
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, "footman", version)
	case "help", "--help", "-h":
		_, _ = fmt.Fprint(stdout, usage)
	default:
		_, _ = fmt.Fprintf(stderr, "footman: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	return 0
}
