//go:build linux

package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// runSpec describes one command to run.
type runSpec struct {
	Name  string
	Args  []string
	Stdin string
	// Capture returns stdout. Otherwise output is discarded, which is needed
	// for wl-copy, whose background child would hold a pipe open forever.
	Capture bool
	// Grace, if set, returns nil when the command is still running after this
	// long (a launcher that stays in the foreground), leaving it to run.
	Grace time.Duration
}

// runResult is what a command produced.
type runResult struct {
	Stdout, Stderr string
}

type runFunc func(ctx context.Context, s runSpec) (runResult, error)

// errNoTool is returned when the program is not installed.
var errNoTool = errors.New("not installed")

func defaultRun(ctx context.Context, s runSpec) (runResult, error) {
	path, err := exec.LookPath(s.Name)
	if err != nil {
		return runResult{}, fmt.Errorf("%s: %w", s.Name, errNoTool)
	}
	var out, errb bytes.Buffer
	var cmd *exec.Cmd
	if s.Grace > 0 {
		// Do not tie a long-running launched app to ctx.
		cmd = exec.Command(path, s.Args...)
	} else {
		cmd = exec.CommandContext(ctx, path, s.Args...)
	}
	if s.Stdin != "" {
		cmd.Stdin = strings.NewReader(s.Stdin)
	}
	if s.Capture {
		cmd.Stdout = &out
		cmd.Stderr = &errb
	}
	if err := cmd.Start(); err != nil {
		return runResult{}, fmt.Errorf("%s: %w", s.Name, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var grace <-chan time.Time
	if s.Grace > 0 {
		grace = time.After(s.Grace)
	}
	select {
	case err := <-done:
		res := runResult{Stdout: out.String(), Stderr: strings.TrimSpace(errb.String())}
		if err != nil {
			return res, fmt.Errorf("%s: %w", s.Name, err)
		}
		return res, nil
	case <-grace:
		return runResult{}, nil
	case <-ctx.Done():
		if s.Grace > 0 {
			return runResult{}, nil // leave it running
		}
		return runResult{}, ctx.Err()
	}
}

// unsupportedTool wraps a missing-tool error with platform.ErrUnsupported.
func unsupportedTool(what, hint string, err error) error {
	return fmt.Errorf("linux %s: %w (%v; %s)", what, platform.ErrUnsupported, err, hint)
}
