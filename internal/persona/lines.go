package persona

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	toml "github.com/pelletier/go-toml/v2"
)

// Kind is a kind of stock line.
type Kind string

// Line kinds (design §27).
const (
	Ack     Kind = "ack"
	Working Kind = "working"
	Done    Kind = "done"
	Confirm Kind = "confirm"
	Error   Kind = "error"
	Quips   Kind = "quips"
)

// Kinds lists every kind, in file order.
var Kinds = []Kind{Ack, Working, Done, Confirm, Error, Quips}

// AddressPlaceholder is filled with the user's address form in a line.
const AddressPlaceholder = "{address}"

type linesFile struct {
	Ack     []string `toml:"ack"`
	Working []string `toml:"working"`
	Done    []string `toml:"done"`
	Confirm []string `toml:"confirm"`
	Error   []string `toml:"error"`
	Quips   []string `toml:"quips"`
}

type lineSet struct{ by map[Kind][]string }

func parseLines(b []byte) (*lineSet, []string) {
	var f linesFile
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var se *toml.StrictMissingError
		if errors.As(err, &se) {
			var errs []string
			for _, d := range se.Errors {
				errs = append(errs, "unknown key "+strings.Join(d.Key(), "."))
			}
			return nil, errs
		}
		return nil, []string{err.Error()}
	}
	ls := &lineSet{by: map[Kind][]string{Ack: f.Ack, Working: f.Working, Done: f.Done, Confirm: f.Confirm, Error: f.Error, Quips: f.Quips}}
	var errs []string
	for _, k := range Kinds {
		l := ls.by[k]
		if len(l) > MaxLinesPerKind {
			errs = append(errs, fmt.Sprintf("%s: more than %d lines", k, MaxLinesPerKind))
		}
		seen := map[string]bool{}
		for i, s := range l {
			l[i] = strings.TrimSpace(s)
			s = l[i]
			switch {
			case s == "":
				errs = append(errs, fmt.Sprintf("%s[%d] is empty", k, i))
			case utf8.RuneCountInString(s) > MaxLineRunes:
				errs = append(errs, fmt.Sprintf("%s[%d] is longer than %d characters", k, i, MaxLineRunes))
			case strings.ContainsAny(s, "\x00\n\r"):
				errs = append(errs, fmt.Sprintf("%s[%d] has control characters", k, i))
			case strings.ContainsAny(strings.ReplaceAll(s, AddressPlaceholder, ""), "{}"):
				errs = append(errs, fmt.Sprintf("%s[%d]: only {address} may appear in braces", k, i))
			case seen[s]:
				errs = append(errs, fmt.Sprintf("%s[%d] repeats an earlier line", k, i))
			}
			seen[s] = true
		}
	}
	return ls, errs
}

// Line is one stock line.
type Line struct {
	Kind Kind
	// Index is the line's position within its kind and language file; the
	// clip bank uses (language, kind, index) to name a clip.
	Index int
	// Text is the template, with {address} unfilled.
	Text string
}

// HasAddress reports whether the line contains {address}. Such lines are
// rendered per user by the clip bank; the others can ship as clips.
func (l Line) HasAddress() bool { return strings.Contains(l.Text, AddressPlaceholder) }

// Render fills {address}.
func (l Line) Render(address string) string {
	return strings.ReplaceAll(l.Text, AddressPlaceholder, address)
}

// langChain is the languages to try for lang: itself, its base ("pt" for
// "pt-BR"), then English.
func langChain(lang string) []string {
	lang = strings.ToLower(strings.ReplaceAll(lang, "_", "-"))
	var c []string
	if lang != "" {
		c = append(c, lang)
		if base, _, ok := strings.Cut(lang, "-"); ok {
			c = append(c, base)
		}
	}
	return append(c, "en")
}

// Lines returns the lines of a kind in lang, falling back to the base
// language and then English when the pack has none.
func (p *Pack) Lines(kind Kind, lang string) []Line {
	for _, l := range langChain(lang) {
		if ls := p.lines[l]; ls != nil && len(ls.by[kind]) > 0 {
			return toLines(kind, ls.by[kind])
		}
	}
	return nil
}

func toLines(kind Kind, ss []string) []Line {
	out := make([]Line, len(ss))
	for i, s := range ss {
		out[i] = Line{Kind: kind, Index: i, Text: s}
	}
	return out
}

// AddressLines returns every line in lang (all kinds) that contains
// {address}, for the clip bank to render per user. Lines without it are
// returned by StaticLines.
func (p *Pack) AddressLines(lang string) []Line { return p.filter(lang, true) }

// StaticLines returns every line in lang that has no {address}, which can be
// pre-generated once.
func (p *Pack) StaticLines(lang string) []Line { return p.filter(lang, false) }

func (p *Pack) filter(lang string, withAddress bool) []Line {
	var out []Line
	for _, k := range Kinds {
		for _, l := range p.Lines(k, lang) {
			if l.HasAddress() == withAddress {
				out = append(out, l)
			}
		}
	}
	return out
}

// Chooser picks lines at random without repeating the one it picked last for
// the same kind, so repeats are not obvious. It is safe for concurrent use.
type Chooser struct {
	mu   sync.Mutex
	rand func(n int) int
	last map[string]string
}

// NewChooser returns a Chooser. rnd(n) returns a number in [0, n); pass
// math/rand/v2's IntN, or a fixed function in tests.
func NewChooser(rnd func(n int) int) *Chooser {
	return &Chooser{rand: rnd, last: map[string]string{}}
}

// Pick returns a line of the kind from p in lang, or false if the pack has
// none. The line is not rendered; call Render with the user's address.
func (c *Chooser) Pick(p *Pack, kind Kind, lang string) (Line, bool) {
	ls := p.Lines(kind, lang)
	if len(ls) == 0 {
		return Line{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := p.ID + "/" + lang + "/" + string(kind)
	last := c.last[key]
	cands := ls
	if len(ls) > 1 {
		cands = nil
		for _, l := range ls {
			if l.Text != last {
				cands = append(cands, l)
			}
		}
	}
	l := cands[c.rand(len(cands))]
	c.last[key] = l.Text
	return l, true
}
