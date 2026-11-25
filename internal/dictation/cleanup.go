package dictation

import (
	"slices"
	"strings"
)

// DefaultFillers are dropped from dictation by the simple tier (English).
var DefaultFillers = []string{"um", "umm", "uh", "uhh", "uhm", "er", "erm", "ah", "hmm", "mm"}

// Edit is a mid-dictation command's effect on the text built so far.
type Edit func(d *Draft)

// DictationCommand is a fixed phrase that edits the text instead of being
// typed (design §17, simple tier).
type DictationCommand struct {
	// Phrase is matched case-insensitively, ignoring punctuation, as whole
	// words.
	Phrase string
	// Apply edits the draft.
	Apply Edit
}

// DefaultDictationCommands are the simple-tier mid-dictation commands
// (English). Add a row to support a new phrase.
var DefaultDictationCommands = []DictationCommand{
	{Phrase: "new paragraph", Apply: func(d *Draft) { d.Break("\n\n") }},
	{Phrase: "new line", Apply: func(d *Draft) { d.Break("\n") }},
	{Phrase: "scratch that", Apply: func(d *Draft) { d.Scratch() }},
}

// Draft is dictated text being assembled word by word. Mid-dictation commands
// edit it.
type Draft struct {
	// tokens are words (with their punctuation) and line breaks.
	tokens []string
	// marks are token counts at which a chunk ended (a sentence end or a
	// command); Scratch removes back to the latest one. marks[0] is 0.
	marks []int
}

func newDraft() *Draft { return &Draft{marks: []int{0}} }

func isBreak(tok string) bool { return strings.Trim(tok, "\n") == "" }

// Word appends a dictated word.
func (d *Draft) Word(w string) {
	d.tokens = append(d.tokens, w)
	if strings.ContainsAny(trailingPunct(w), ".?!") {
		d.mark()
	}
}

// Break appends a line break ("\n" or "\n\n") and ends the current chunk.
func (d *Draft) Break(s string) {
	d.tokens = append(d.tokens, s)
	d.mark()
}

// Scratch removes the current chunk: the words since the last sentence end,
// line break or command. If the current chunk is empty, it removes the one
// before it. It only reaches text dictated in the same utterance.
func (d *Draft) Scratch() {
	n := len(d.tokens)
	if d.marks[len(d.marks)-1] == n && len(d.marks) > 1 {
		d.marks = d.marks[:len(d.marks)-1]
	}
	d.tokens = d.tokens[:d.marks[len(d.marks)-1]]
	d.mark()
}

func (d *Draft) mark() {
	if n := len(d.tokens); d.marks[len(d.marks)-1] != n {
		d.marks = append(d.marks, n)
	}
}

// last returns the last word, skipping nothing, or "" if the draft is empty or
// ends with a line break.
func (d *Draft) last() string {
	if len(d.tokens) == 0 || isBreak(d.tokens[len(d.tokens)-1]) {
		return ""
	}
	return d.tokens[len(d.tokens)-1]
}

// String renders the draft: words separated by single spaces, with no spaces
// around line breaks.
func (d *Draft) String() string {
	var b strings.Builder
	for i, t := range d.tokens {
		if i > 0 && !isBreak(t) && !isBreak(d.tokens[i-1]) {
			b.WriteByte(' ')
		}
		b.WriteString(t)
	}
	return strings.Trim(b.String(), " ")
}

// Cleaner is the simple-tier dictation cleanup: it drops filler words and
// immediately repeated words, and applies mid-dictation commands. The smart
// tier will replace it behind the same Clean method.
type Cleaner struct {
	fillers  map[string]bool
	commands []cleanCommand
}

type cleanCommand struct {
	words []string
	apply Edit
}

// NewCleaner builds a cleaner. nil fillers or commands use DefaultFillers and
// DefaultDictationCommands.
func NewCleaner(fillers []string, commands []DictationCommand) *Cleaner {
	if fillers == nil {
		fillers = DefaultFillers
	}
	if commands == nil {
		commands = DefaultDictationCommands
	}
	c := &Cleaner{fillers: map[string]bool{}}
	for _, f := range fillers {
		c.fillers[Norm(f)] = true
	}
	for _, cmd := range commands {
		if w := NormWords(cmd.Phrase); len(w) > 0 {
			c.commands = append(c.commands, cleanCommand{words: w, apply: cmd.Apply})
		}
	}
	// Longest phrase first, so a longer command wins over a prefix of it.
	slices.SortStableFunc(c.commands, func(a, b cleanCommand) int { return len(b.words) - len(a.words) })
	return c
}

// Clean returns text ready to insert.
func (c *Cleaner) Clean(text string) string {
	d := newDraft()
	tokens := strings.Fields(text)
	for i := 0; i < len(tokens); {
		if n := c.command(d, tokens[i:]); n > 0 {
			i += n
			continue
		}
		w := tokens[i]
		i++
		nw := Norm(w)
		switch {
		case nw == "":
			// Stray punctuation, such as a dash left between fillers.
			continue
		case c.fillers[nw]:
			// Keep a sentence end the filler carried: "that's it, um." -> "that's it."
			if p := trailingPunct(w); strings.ContainsAny(p, ".?!") {
				if l := d.last(); l != "" && !strings.ContainsAny(trailingPunct(l), ".?!") {
					d.tokens[len(d.tokens)-1] = strings.TrimRight(l, ",;:") + p
					d.mark()
				}
			}
			continue
		}
		if l := d.last(); l != "" && trailingPunct(l) == "" && Norm(l) == nw {
			// "the the" -> "the"; keep the later copy's punctuation.
			d.tokens = d.tokens[:len(d.tokens)-1]
		}
		d.Word(w)
	}
	return d.String()
}

// command applies the mid-dictation command at the start of tokens, if any,
// and returns how many tokens it used.
func (c *Cleaner) command(d *Draft, tokens []string) int {
	for _, cmd := range c.commands {
		if HasPrefixWords(tokens, cmd.words) {
			cmd.apply(d)
			return len(cmd.words)
		}
	}
	return 0
}
