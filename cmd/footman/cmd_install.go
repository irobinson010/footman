package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/platform"
)

// Seams for tests: the input, the platform and the path of the running program.
var (
	stdin       io.Reader = os.Stdin
	executable            = os.Executable
	newPlatform           = func() (platform.Platform, error) { return platform.Platforms.New(runtime.GOOS, nil) }
)

// terminalFD returns the file descriptor of r when it is an interactive
// terminal.
func terminalFD(r io.Reader) (int, bool) {
	f, ok := r.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0, false
	}
	return int(f.Fd()), true
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("footman "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "footman: %v\n", err)
	return 1
}

// cmdSetup is `footman setup` (design §11).
func cmdSetup(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("setup", stderr)
	var o install.SetupOptions
	var keyStdin, nonInteractive bool
	fs.StringVar(&o.Name, "name", "", "what the assistant calls you")
	fs.StringVar(&o.Persona, "persona", "", "default persona")
	fs.StringVar(&o.Provider, "provider", "", "AI provider ("+strings.Join(install.Providers, ", ")+")")
	fs.BoolVar(&keyStdin, "api-key-stdin", false, "read the provider's API key from standard input")
	fs.StringVar(&o.Hotkey, "hotkey", "", "push-to-talk key (default "+install.DefaultHotkey+")")
	fs.BoolVar(&o.NoService, "no-service", false, "do not install the background service")
	fs.BoolVar(&nonInteractive, "non-interactive", false, "never ask questions")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "footman setup: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	in := bufio.NewReader(stdin)
	if keyStdin {
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fail(stderr, err)
		}
		if o.APIKey = strings.TrimSpace(line); o.APIKey == "" {
			return fail(stderr, errors.New("--api-key-stdin: no key on standard input"))
		}
	}
	p, err := newPlatform()
	if err != nil {
		return fail(stderr, err)
	}
	o.Platform = p
	if exe, err := executable(); err == nil {
		o.Binary = exe
	}

	if fd, ok := terminalFD(stdin); ok && !nonInteractive {
		pr := install.NewPrompter(in, stdout, func() (string, error) {
			b, err := term.ReadPassword(fd)
			return string(b), err
		})
		if err := pr.AskSetup(&o); err != nil {
			return fail(stderr, err)
		}
	}

	res, err := install.Setup(context.Background(), o)
	if res != nil {
		for _, l := range res.Lines {
			_, _ = fmt.Fprintln(stdout, l)
		}
	}
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "\nSetup complete. Receipt: %s\nUndo it all with: footman uninstall\n", res.ReceiptPath)
	return 0
}

// cmdUninstall is `footman uninstall` (design §11).
func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("uninstall", stderr)
	o := install.UninstallOptions{}
	var nonInteractive bool
	fs.BoolVar(&o.Purge, "purge", false, "remove everything, keychain secrets included, without asking")
	fs.BoolVar(&o.KeepConfig, "keep-config", false, "keep config.toml")
	fs.BoolVar(&o.KeepData, "keep-data", false, "keep history, jobs, personas and the install receipt")
	fs.BoolVar(&o.RemoveSecrets, "remove-secrets", false, "also delete the API keys from the OS keychain")
	fs.BoolVar(&o.DryRun, "dry-run", false, "show what would be removed and change nothing")
	fs.BoolVar(&nonInteractive, "non-interactive", false, "never ask questions")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "footman uninstall: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if o.Purge && (o.KeepConfig || o.KeepData) {
		_, _ = fmt.Fprintln(stderr, "footman uninstall: --purge cannot be combined with --keep-config or --keep-data")
		return 2
	}

	p, err := newPlatform()
	if err != nil {
		return fail(stderr, err)
	}
	o.Platform = p
	pl, err := install.LoadPlan(p)
	if errors.Is(err, install.ErrNoReceipt) {
		_, _ = fmt.Fprintf(stderr, "footman: nothing to uninstall: %v\n", err)
		return 1
	}
	if err != nil {
		return fail(stderr, err)
	}

	decided := o.Purge || o.KeepConfig || o.KeepData
	if _, ok := terminalFD(stdin); ok && !nonInteractive {
		pr := install.NewPrompter(stdin, stdout, nil)
		proceed, err := pr.AskUninstall(pl, &o, decided)
		if err != nil {
			return fail(stderr, err)
		}
		if !proceed {
			_, _ = fmt.Fprintln(stdout, "Nothing was changed.")
			return 1
		}
	} else {
		if !decided {
			_, _ = fmt.Fprintln(stderr, "footman uninstall: no terminal to ask what to keep; pass --purge, or --keep-config and/or --keep-data (add --dry-run to preview)")
			return 2
		}
		_, _ = fmt.Fprintln(stdout, "Footman will remove:")
		pl.Listing(stdout)
		_, _ = fmt.Fprintln(stdout)
	}

	rep, err := pl.Run(context.Background(), o)
	if rep != nil {
		rep.Write(stdout)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}
