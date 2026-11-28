package core

import (
	"regexp"
	"strings"

	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/platform"
)

// startKind is what a dictation start phrase asks for.
type startKind int

const (
	// startFocused types into the focused app.
	startFocused startKind = iota + 1
	// startNewNote creates a timestamped .txt, opens it and types into it.
	startNewNote
	// startNewDoc is startNewNote with a .md file.
	startNewDoc
	// startOpenFile finds the named file, opens it and types into it.
	startOpenFile
	// startStop is "stop dictation" said while no session runs.
	startStop
)

// startSpec is a recognized start phrase.
type startSpec struct {
	kind startKind
	// file is the spoken file name for startOpenFile.
	file string
}

var focusedStarts = map[string]bool{
	// Speech-to-text often hears "Alfred, write" as the name "Alfred Wright"
	// ("right" is left out: it answers a question in the follow-up window).
	"write": true, "wright": true, "rite": true, "write it": true,
	"dictate": true, "dictation": true, "start writing": true, "begin writing": true, "take a note": true, "start dictation": true,
	"start dictating": true, "begin dictation": true, "begin dictating": true,
}

var (
	newNoteRE  = regexp.MustCompile(`^(?:(?:write|wright|rite|dictate|take|start|make|create|open) (?:(?:in|into|on|a|an|the) )*)?new (note|document|doc)$`)
	openFileRE = regexp.MustCompile(`^open (.+?) and (?:write|wright|rite|dictate|start dictation|start dictating|take a note|type)(?: (?:in|into|to) it)?$`)
)

// matchStart recognizes a dictation start phrase in the words of a command
// (normalized, the wake word already removed). The whole command must be the
// phrase, so "write hello world" is still a one-shot dictation.
func matchStart(words []string) (startSpec, bool) {
	for len(words) > 0 && words[0] == "please" {
		words = words[1:]
	}
	for len(words) > 0 && words[len(words)-1] == "please" {
		words = words[:len(words)-1]
	}
	s := strings.Join(words, " ")
	switch {
	case focusedStarts[s]:
		return startSpec{kind: startFocused}, true
	case s == "stop dictation" || s == "end dictation":
		return startSpec{kind: startStop}, true
	}
	if m := newNoteRE.FindStringSubmatch(s); m != nil {
		if m[1] == "note" {
			return startSpec{kind: startNewNote}, true
		}
		return startSpec{kind: startNewDoc}, true
	}
	if m := openFileRE.FindStringSubmatch(s); m != nil {
		return startSpec{kind: startOpenFile, file: m[1]}, true
	}
	return startSpec{}, false
}

// editCmd is a spoken navigation or editing command: key presses sent to the
// focused app (ADR-0045).
type editCmd struct {
	// keys are pressed in order.
	keys []string
	// terminal says whether the command may run while a terminal has
	// focus. Enter never may (ADR-0032), and neither may the shortcuts a
	// terminal reads as signals or flow control (Ctrl+Z suspends the
	// running program, Ctrl+S freezes the output).
	terminal bool
}

var editCommands = func() map[string]editCmd {
	m := map[string]editCmd{}
	add := func(c editCmd, phrases ...string) {
		for _, p := range phrases {
			m[p] = c
		}
	}
	add(editCmd{keys: []string{"Ctrl+End"}}, "go to the end", "go to end", "go to the end of the document", "go to the bottom")
	add(editCmd{keys: []string{"Ctrl+Home"}}, "go to the start", "go to the beginning", "go to the start of the document", "go to the top")
	add(editCmd{keys: []string{platform.KeyEnd}, terminal: true}, "end of line", "go to the end of the line", "go to end of line")
	add(editCmd{keys: []string{platform.KeyHome}, terminal: true}, "start of line", "beginning of line", "go to the start of the line", "go to start of line")
	add(editCmd{keys: []string{"Ctrl+A"}}, "select all")
	add(editCmd{keys: []string{"Ctrl+Z"}}, "undo", "undo that")
	add(editCmd{keys: []string{"Ctrl+Shift+Z"}}, "redo", "redo that")
	add(editCmd{keys: []string{platform.KeyEnter}}, "press enter", "press return", "hit enter")
	add(editCmd{keys: []string{platform.KeyTab}, terminal: true}, "press tab", "hit tab")
	add(editCmd{keys: []string{platform.KeyEscape}, terminal: true}, "press escape", "press escape key", "hit escape")
	add(editCmd{keys: []string{"Ctrl+S"}}, "save", "save it", "save file", "save the file", "save document")
	return m
}()

// matchEdit recognizes a whole utterance, as normalized words, that is an
// editing command.
func matchEdit(words []string) (editCmd, bool) {
	for len(words) > 0 && words[len(words)-1] == "please" {
		words = words[:len(words)-1]
	}
	c, ok := editCommands[strings.Join(words, " ")]
	return c, ok
}

// DefaultDictationStopPhrases end a dictation session when they are the whole
// utterance (config dictation.stop_phrases). "<wake word> stop" always does,
// as does the break-glass stop.
var DefaultDictationStopPhrases = []string{"stop dictation", "end dictation", "that's all"}

// straightApostrophes makes the curly apostrophes speech-to-text sometimes
// writes the plain ones the phrase lists use.
var straightApostrophes = strings.NewReplacer("\u2019", "'", "\u2018", "'")

// stopMatcher recognizes the stop phrases of a session.
type stopMatcher struct {
	r       *Router
	phrases [][]string
}

// match reports whether text ends the session. The whole utterance must be a
// stop phrase, with an optional wake word in front and a trailing period
// ("Stop dictation."), so "please stop dictation errors in the log" is typed.
// A stop phrase that ends a longer utterance, after a sentence break ("…and
// that's the end. Stop dictation."), also ends the session; rest is then what
// came before it, to be typed.
func (m stopMatcher) match(text string) (rest string, stop bool) {
	text = straightApostrophes.Replace(text) // "that\u2019s all"
	words := dictation.NormWords(text)
	if len(words) == 0 {
		return "", false
	}
	if m.whole(words) {
		return "", true
	}
	if after, ok := m.r.MatchWake(text); ok {
		w := dictation.NormWords(after)
		if len(w) == 1 && w[0] == "stop" || m.whole(w) {
			return "", true
		}
	}
	// A stop phrase after a sentence break at the end of a longer utterance.
	m.r.mu.RLock()
	wake := m.r.wake
	m.r.mu.RUnlock()
	phrases := slicesOf(m.phrases)
	for _, w := range wake {
		for _, p := range slicesOf(m.phrases, []string{"stop"}) {
			phrases = append(phrases, append(append([]string{}, w...), p...))
		}
	}
	tokens := strings.Fields(text)
	var idx []int // tokens that are words
	for i, t := range tokens {
		if dictation.Norm(t) != "" {
			idx = append(idx, i)
		}
	}
	best := -1
	for _, p := range phrases {
		n := len(p)
		if len(idx) <= n {
			continue
		}
		ok := true
		for k := range n {
			if dictation.Norm(tokens[idx[len(idx)-n+k]]) != p[k] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		prev := tokens[idx[len(idx)-n-1]]
		if !strings.ContainsAny(prev[len(prev)-1:], ".!?,;") {
			continue
		}
		if start := idx[len(idx)-n]; best < 0 || start < best {
			best = start
		}
	}
	if best < 0 {
		return "", false
	}
	rest = strings.TrimRight(strings.Join(tokens[:best], " "), ",;")
	return rest, true
}

// whole reports whether the normalized words are exactly a stop phrase, or
// the wake word followed by one.
func (m stopMatcher) whole(words []string) bool {
	for _, p := range m.phrases {
		if equalWords(words, p) {
			return true
		}
	}
	return false
}

func equalWords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func slicesOf(ps [][]string, extra ...[]string) [][]string {
	return append(append([][]string{}, ps...), extra...)
}

// pickNumber recognizes "one", "2", "the third one" and similar answers to
// "which file?", as an index from 0, or false.
func pickNumber(words []string) (int, bool) {
	words = dropWords(words, "the", "number", "please", "file")
	if len(words) == 2 && words[1] == "one" {
		words = words[:1]
	}
	if len(words) != 1 {
		return 0, false
	}
	switch words[0] {
	case "1", "one", "first", "1st":
		return 0, true
	case "2", "two", "second", "2nd":
		return 1, true
	case "3", "three", "third", "3rd":
		return 2, true
	}
	return 0, false
}
