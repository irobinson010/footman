package core

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/irobinson010/footman/internal/plugins"
)

// StartDictationTool is the name of the built-in tool that starts a dictation
// session for the AI, for the requests the fast path did not recognize
// because speech-to-text misheard them ("Alfred Wright", "let's do some
// dictation") (ADR-0045).
const StartDictationTool = "start_dictation"

// startDictationTool maps the AI's call onto the same startSpec the fast-path
// DictationRule runs. It is Act, like the rule, so after untrusted content it
// needs a confirmation (ADR-0047). It is offered only when the always-on
// microphone is running.
type startDictationTool struct{ p *Pipeline }

func (startDictationTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{
		Name: StartDictationTool,
		Description: "Start voice dictation: from then on what the user says is typed as text, until they say \"stop dictation\". " +
			"Call it whenever they ask to write, dictate, type, take a note or start a document, in any wording. " +
			"target is \"focused\" (the default) to type into the app that has focus, \"new_note\" for a new text note, " +
			"\"new_document\" for a new Markdown document, or \"file\" to open an existing file by its spoken name (set file).",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"target":{"type":"string","enum":["focused","new_note","new_document","file"],"description":"Where the text goes. Default focused."},` +
			`"file":{"type":"string","description":"The spoken name of the file to open, when target is file."}}}`),
		Permission: plugins.Act,
	}
}

func (t startDictationTool) Run(ctx context.Context, args json.RawMessage) (plugins.Result, error) {
	var a struct {
		Target string `json:"target"`
		File   string `json:"file"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return plugins.Result{Text: "target must be focused, new_note, new_document or file", IsError: true}, nil
		}
	}
	var spec startSpec
	switch strings.ToLower(strings.TrimSpace(a.Target)) {
	case "", "focused":
		spec = startSpec{kind: startFocused}
	case "new_note":
		spec = startSpec{kind: startNewNote}
	case "new_document":
		spec = startSpec{kind: startNewDoc}
	case "file":
		name := strings.TrimSpace(a.File)
		if name == "" {
			return plugins.Result{Text: "file is required when target is file", IsError: true}, nil
		}
		spec = startSpec{kind: startOpenFile, file: name}
	default:
		return plugins.Result{Text: "target must be focused, new_note, new_document or file", IsError: true}, nil
	}
	msg, err := t.p.startDictation(ctx, spec)
	if err != nil {
		return plugins.Result{}, err
	}
	if msg != "" {
		// Not started (or waiting for a file choice): the words are what
		// the user should hear, so the AI can pass them on.
		return plugins.Result{Text: msg, IsError: true}, nil
	}
	return plugins.Result{Text: "Dictation has started. Reply with at most two or three words, such as \"Go ahead\"; the user ends it by saying \"stop dictation\"."}, nil
}

// DictationRules is the part of the system prompt about starting dictation. It
// is empty when there is no always-on microphone, where the tool is not
// offered. Nothing in it changes between requests, so providers can cache it.
func DictationRules(on bool) string {
	if !on {
		return ""
	}
	return `When the user asks to write, dictate, type, take a note or start a document, in any wording (speech-to-text often mishears these requests), call start_dictation instead of answering. Use target new_note or new_document for "a new note" or "a new document", and file with the spoken name when they name an existing file; otherwise leave target out to type into the app they are using. Once it has started, answer in a few words at most, and know that the user ends dictation by saying "stop dictation".`
}
