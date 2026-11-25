package dictation

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/irobinson010/footman/internal/platform"
)

// InsertMode is how dictated text reaches the focused app (config
// `dictation.insert`, ADR-0022).
type InsertMode string

// Insert modes.
const (
	// InsertAuto types short single-line text whose characters are all on
	// the keyboard layout, and pastes everything else.
	InsertAuto InsertMode = "auto"
	// InsertType always types.
	InsertType InsertMode = "type"
	// InsertPaste always pastes.
	InsertPaste InsertMode = "paste"
)

// DefaultTypeMax is the longest text, in characters, that auto mode types
// rather than pastes. Typing runs at about 1,000 characters/s (ADR-0022), so
// this keeps typing under a fifth of a second.
const DefaultTypeMax = 200

// Layout reports whether text can be typed on the current keyboard layout.
// Typed keysyms that are not on the layout are dropped by some apps
// (ADR-0022), so auto mode pastes such text instead. The platform layer will
// provide a real implementation; ASCIILayout stands in until then.
type Layout interface {
	Typeable(text string) bool
}

// ASCIILayout treats printable ASCII and spaces as typeable, a safe
// approximation of a US layout.
type ASCIILayout struct{}

// Typeable implements Layout.
func (ASCIILayout) Typeable(text string) bool {
	for _, r := range text {
		if r < ' ' || r > '~' {
			return false
		}
	}
	return true
}

// InsertMethod says how one piece of text was inserted.
type InsertMethod string

// Insert methods.
const (
	MethodType  InsertMethod = "type"
	MethodPaste InsertMethod = "paste"
)

// ChooseInsert applies ADR-0022's rule: in auto mode, type text that is short
// (at most typeMax characters), has no newline and is all typeable on the
// layout; paste everything else.
func ChooseInsert(mode InsertMode, text string, layout Layout, typeMax int) InsertMethod {
	switch mode {
	case InsertType:
		return MethodType
	case InsertPaste:
		return MethodPaste
	}
	if utf8.RuneCountInString(text) <= typeMax && !strings.ContainsAny(text, "\r\n") && layout.Typeable(text) {
		return MethodType
	}
	return MethodPaste
}

// Insert puts text at the cursor with method m.
func Insert(ctx context.Context, in platform.Input, m InsertMethod, text string) error {
	switch m {
	case MethodType:
		return in.Type(ctx, text)
	case MethodPaste:
		return in.Paste(ctx, text)
	}
	return fmt.Errorf("dictation: unknown insert method %q", m)
}
