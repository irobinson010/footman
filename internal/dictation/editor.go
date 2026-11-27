package dictation

import (
	"context"
	"fmt"

	"github.com/irobinson010/footman/internal/platform"
)

// TextEditor is the seam for text-aware editing during a voice-started
// dictation session (ADR-0045): "replace X with Y" and "after this sentence
// write Z". Both need to read the text in the focused app, which key presses
// cannot do, so another design (accessibility, AT-SPI) will implement them.
// Until then NoEditor, which the session uses by default, answers every call
// with an error wrapping platform.ErrUnsupported.
type TextEditor interface {
	// Replace replaces the text old, as the user has it in the focused
	// document, with new.
	Replace(ctx context.Context, old, new string) error
	// InsertAfter inserts text after the text anchor in the focused
	// document.
	InsertAfter(ctx context.Context, anchor, text string) error
}

type noEditor struct{}

func (noEditor) Replace(context.Context, string, string) error {
	return fmt.Errorf("dictation: replacing text: %w", platform.ErrUnsupported)
}

func (noEditor) InsertAfter(context.Context, string, string) error {
	return fmt.Errorf("dictation: inserting after text: %w", platform.ErrUnsupported)
}

// NoEditor is the TextEditor that supports nothing.
var NoEditor TextEditor = noEditor{}
