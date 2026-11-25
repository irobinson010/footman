//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/irobinson010/footman/internal/platform"
)

// Clipboard reads and writes the Wayland clipboard with wl-paste and wl-copy
// (wl-clipboard), which use the data-control protocol KWin and wlroots offer.
type Clipboard struct {
	run runFunc
}

const clipboardHint = "install wl-clipboard; X11-only sessions are not supported yet"

// ReadText returns the clipboard text, or "" if it is empty or not text.
func (c *Clipboard) ReadText(ctx context.Context) (string, error) {
	res, err := c.run(ctx, runSpec{Name: "wl-paste", Args: []string{"--no-newline", "--type", "text"}, Capture: true})
	switch {
	case err == nil:
		return res.Stdout, nil
	case errors.Is(err, errNoTool):
		return "", unsupportedTool("clipboard", clipboardHint, err)
	case strings.Contains(res.Stderr, "Nothing is copied"), strings.Contains(res.Stderr, "No selection"),
		strings.Contains(res.Stderr, "No suitable type"):
		return "", nil
	}
	return "", fmt.Errorf("linux clipboard: read: %w: %s", err, res.Stderr)
}

// WriteText replaces the clipboard contents with text. An empty text clears it.
func (c *Clipboard) WriteText(ctx context.Context, text string) error {
	args := []string{"--type", "text/plain;charset=utf-8"}
	if text == "" {
		args = []string{"--clear"}
	}
	_, err := c.run(ctx, runSpec{Name: "wl-copy", Args: args, Stdin: text})
	if errors.Is(err, errNoTool) {
		return unsupportedTool("clipboard", clipboardHint, err)
	}
	if err != nil {
		return fmt.Errorf("linux clipboard: write: %w", err)
	}
	return nil
}

var _ platform.Clipboard = (*Clipboard)(nil)
