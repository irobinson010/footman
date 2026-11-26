package dictation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// FormatState is what the formatter needs to know about the focus.
type FormatState struct {
	// Terminal is whether the focused app is a terminal. Enter is never
	// produced there (ADR-0032).
	Terminal bool
	// Code is whether code mode is on.
	Code bool
}

// FormatResult is a formatted utterance.
type FormatResult struct {
	Text string
	// ScratchPrev is set when "scratch that" had nothing earlier in the
	// utterance to remove: the caller should take back the previous
	// utterance's inserted text before typing Text.
	ScratchPrev bool
	// Code is the code mode at the end of the utterance, after any
	// "command mode" / "normal mode" in it.
	Code bool
}

type kind int

const (
	kWord kind = iota
	kLetter
	kSymbol
	kSpace
	kBreak
)

type piece struct {
	text                string
	kind                kind
	glueLeft, glueRight bool
}

// Formatter applies spoken formatting commands, spoken punctuation and code
// mode to a transcript (ADR-0032). It is deterministic and has no state.
type Formatter struct {
	lang   *Lang
	maxLen int // words in the longest phrase
}

// NewFormatter builds a formatter for a language code ("en"); unknown codes
// use English.
func NewFormatter(lang string) *Formatter {
	l := Langs[lang]
	if l == nil {
		l = Langs["en"]
	}
	f := &Formatter{lang: l, maxLen: 1}
	count := func(s string) { f.maxLen = max(f.maxLen, len(strings.Fields(s))) }
	for p := range l.Commands {
		count(p)
	}
	for p := range l.Punct {
		count(p)
	}
	for p := range l.Symbols {
		count(p)
	}
	return f
}

type match struct {
	n     int // words used
	cmd   Cmd
	punct Punct
	sym   string
	words []string // the phrase's words, for the literal escape
}

// lookup finds the longest phrase at the start of words.
func (f *Formatter) lookup(words []string, code bool) (m match, ok bool) {
	for n := min(f.maxLen, len(words)); n >= 1; n-- {
		phrase := strings.Join(words[:n], " ")
		m = match{n: n, words: words[:n]}
		if c, found := f.lang.Commands[phrase]; found {
			m.cmd = c
			return m, true
		}
		if code {
			if s, found := f.lang.Symbols[phrase]; found {
				m.sym = s
				return m, true
			}
		} else if p, found := f.lang.Punct[phrase]; found {
			m.punct = p
			return m, true
		}
	}
	return match{}, false
}

// Format formats text. Phrases match case-insensitively and ignore the
// punctuation Whisper puts around them ("New paragraph." is "new paragraph").
func (f *Formatter) Format(text string, st FormatState) FormatResult {
	tokens := strings.Fields(text)
	words := make([]string, len(tokens)) // normalized, for matching
	for i, t := range tokens {
		words[i] = Norm(t)
	}
	var (
		pieces  []piece
		marks   = []int{0} // piece counts at chunk ends, for "scratch that"
		res     FormatResult
		caps    bool
		code    = st.Code
		fillers = map[string]bool{}
	)
	for _, w := range DefaultFillers {
		fillers[w] = true
	}
	mark := func() {
		if n := len(pieces); marks[len(marks)-1] != n {
			marks = append(marks, n)
		}
	}
	add := func(p piece) {
		if caps && (p.kind == kWord || p.kind == kLetter) {
			p.text = strings.ToUpper(p.text)
		}
		pieces = append(pieces, p)
	}
	for i := 0; i < len(tokens); {
		if words[i] == "" {
			i++ // stray punctuation
			continue
		}
		if m, ok := f.lookup(words[i:], code); ok {
			rest := words[i+m.n:]
			i += m.n
			switch {
			case m.cmd == CmdLiteral:
				// Type the next phrase as plain words.
				if lm, ok := f.lookup(rest, code); ok {
					for j := 0; j < lm.n; j++ {
						add(f.word(tokens[i+j], words[i+j], code))
					}
					i += lm.n
				} else {
					add(f.word(tokens[i-1], words[i-1], code))
				}
			case m.cmd == CmdNewLine, m.cmd == CmdNewParagraph:
				if st.Terminal {
					break // never Enter in a terminal
				}
				s := "\n"
				if m.cmd == CmdNewParagraph {
					s = "\n\n"
				}
				add(piece{text: s, kind: kBreak})
				mark()
			case m.cmd == CmdScratch:
				if len(pieces) == 0 {
					res.ScratchPrev = true
					break
				}
				if marks[len(marks)-1] == len(pieces) && len(marks) > 1 {
					marks = marks[:len(marks)-1]
				}
				pieces = pieces[:marks[len(marks)-1]]
				mark()
			case m.cmd == CmdCapsOn:
				caps = true
			case m.cmd == CmdCapsOff:
				caps = false
			case m.cmd == CmdCodeMode:
				code = true
			case m.cmd == CmdTextMode:
				code = false
			case m.cmd == CmdSpace:
				if code {
					add(piece{text: " ", kind: kSpace})
				} else {
					add(f.word(tokens[i-1], words[i-1], code))
				}
			case m.cmd == CmdCapLetter:
				if r, ok := singleRune(rest); ok {
					add(piece{text: string(unicode.ToUpper(r)), kind: kLetter})
					i++
				} else {
					add(f.word(tokens[i-1], words[i-1], code))
				}
			case m.sym != "":
				add(piece{text: m.sym, kind: kSymbol})
			default: // spoken punctuation, text mode
				// "Hello, period" is "Hello.": replace the punctuation
				// Whisper put on the word before.
				if n := len(pieces); n > 0 && pieces[n-1].kind == kWord {
					pieces[n-1].text = strings.TrimRight(pieces[n-1].text, ".,;:!?")
				}
				add(piece{text: m.punct.Text, kind: kSymbol, glueLeft: m.punct.GlueLeft, glueRight: m.punct.GlueRight})
				if strings.ContainsAny(m.punct.Text, ".?!") {
					mark()
				}
			}
			continue
		}
		tok, w := tokens[i], words[i]
		i++
		if fillers[w] {
			continue
		}
		if code {
			// "A.U.X." as ASR spells it out: letters.
			if ls, ok := dottedLetters(tok); ok {
				for _, r := range ls {
					add(piece{text: string(r), kind: kLetter})
				}
				continue
			}
			add(f.word(tok, w, true))
			continue
		}
		add(f.word(tok, w, false))
		if strings.ContainsAny(trailingPunct(tok), ".?!") {
			mark()
		}
	}
	res.Text = render(pieces, code)
	res.Code = code
	if st.Terminal {
		// Last line of defence: no line break reaches a terminal.
		res.Text = strings.NewReplacer("\r", " ", "\n", " ").Replace(res.Text)
	}
	return res
}

// word is a dictated word: in code mode lower case without ASR punctuation;
// in text mode as the engine wrote it.
func (f *Formatter) word(tok, norm string, code bool) piece {
	if !code {
		return piece{text: tok, kind: kWord}
	}
	if utf8.RuneCountInString(norm) == 1 {
		return piece{text: norm, kind: kLetter}
	}
	return piece{text: norm, kind: kWord}
}

func singleRune(words []string) (rune, bool) {
	if len(words) == 0 || utf8.RuneCountInString(words[0]) != 1 {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(words[0])
	return r, unicode.IsLetter(r) || unicode.IsDigit(r)
}

// dottedLetters reads "A.U.X." or "a.u.x" as its letters.
func dottedLetters(tok string) ([]rune, bool) {
	if !strings.Contains(tok, ".") {
		return nil, false
	}
	var out []rune
	for _, part := range strings.Split(strings.Trim(tok, "."), ".") {
		r, ok := singleRune([]string{strings.ToLower(part)})
		if !ok {
			return nil, false
		}
		out = append(out, r)
	}
	return out, len(out) > 1
}

// render joins pieces. Text mode puts one space between words and none next
// to line breaks or glued punctuation. Code mode puts a space only between
// two words (letters join to letters; symbols join to everything); "space" is
// an explicit piece.
func render(pieces []piece, code bool) string {
	var b strings.Builder
	for i, p := range pieces {
		if i > 0 {
			prev := pieces[i-1]
			var sp bool
			if code {
				word := func(k kind) bool { return k == kWord || k == kLetter }
				sp = word(prev.kind) && word(p.kind) && (prev.kind != kLetter || p.kind != kLetter)
			} else {
				sp = prev.kind != kBreak && p.kind != kBreak && !prev.glueRight && !p.glueLeft
			}
			if sp {
				b.WriteByte(' ')
			}
		}
		b.WriteString(p.text)
	}
	if code {
		return b.String()
	}
	return strings.Trim(b.String(), " ")
}
