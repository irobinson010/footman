package sherpa

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// Grapheme-to-phoneme conversion. sherpa-onnx 2.x no longer links espeak-ng,
// so Footman turns words into phonemes itself, from pronunciation lexicons,
// and hands the engine phoneme codepoints (ADR-0037). Kokoro models use the
// "misaki" phoneme alphabet; Piper (VITS) models use espeak-ng's IPA. Words
// missing from the model's own lexicon are looked up in a fallback lexicon in
// espeak IPA (converted for Kokoro), and spelled out letter by letter if all
// else fails, so no word is silently dropped.

// lexicon maps a lower-case word to its phonemes.
type lexicon map[string]string

// loadLexicon reads a lexicon file in either of sherpa-onnx's formats:
// "word p h o n e s" (Kokoro) or "word || p h o n e s" (lexicon-en-us.txt).
// Phrases of several words are skipped; the first entry for a word wins.
func loadLexicon(path string) (lexicon, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	l, err := parseLexicon(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

func parseLexicon(r io.Reader) (lexicon, error) {
	l := lexicon{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var word, phones string
		if w, p, ok := strings.Cut(line, "||"); ok {
			word = strings.TrimSpace(w)
			if strings.ContainsRune(word, ' ') {
				continue
			}
			phones = strings.Join(strings.Fields(p), "")
		} else {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			word, phones = f[0], strings.Join(f[1:], "")
		}
		word = strings.ToLower(word)
		if _, dup := l[word]; !dup && phones != "" {
			l[word] = phones
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(l) == 0 {
		return nil, fmt.Errorf("no entries")
	}
	return l, nil
}

// loadTokens reads a model's tokens.txt ("symbol id" per line) and returns
// the single-codepoint symbols it knows.
func loadTokens(path string) (map[rune]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseTokens(string(raw)), nil
}

func parseTokens(s string) map[rune]bool {
	known := map[rune]bool{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		sym := []rune(line[:i])
		switch len(sym) {
		case 0:
			known[' '] = true
		case 1:
			known[sym[0]] = true
		}
	}
	return known
}

// accent is the variety of English a voice speaks.
type accent int

const (
	american accent = iota
	british
)

// g2p turns a sentence into phonemes for one model and accent.
type g2p struct {
	// primary are lexicons in the model's own alphabet, in order.
	primary []lexicon
	// fallback is a lexicon in espeak IPA, converted with convert.
	fallback lexicon
	// convert maps espeak IPA to the model's alphabet (identity for Piper).
	convert func(string) string
	// tokens are the codepoints the model knows (nil: all).
	tokens map[rune]bool
	// article is how the word "a" is said.
	article string
}

func newG2P(misaki bool, acc accent, primary []lexicon, fallback lexicon, tokens map[rune]bool) *g2p {
	g := &g2p{primary: primary, fallback: fallback, tokens: tokens, article: "ɐ", convert: func(s string) string { return s }}
	if misaki {
		g.convert = func(s string) string { return espeakToMisaki(s, acc) }
	}
	return g
}

// phonemize returns the phoneme codepoints of one sentence: words separated
// by spaces, with the sentence punctuation the model knows.
func (g *g2p) phonemize(sentence string) []rune {
	var b strings.Builder
	first := true
	for _, t := range tokenize(sentence) {
		if t.punct {
			p := t.text
			if !g.knows(p) {
				p = map[string]string{"—": ",", "…": ".", ";": ",", ":": ","}[p]
			}
			if p == "" || !g.knows(p) {
				continue
			}
			out := strings.TrimRight(b.String(), " ")
			b.Reset()
			b.WriteString(out + p + " ")
			continue
		}
		ph := g.word(t.text, first)
		first = false
		if ph == "" {
			continue
		}
		b.WriteString(ph)
		b.WriteByte(' ')
	}
	var out []rune
	for _, r := range strings.TrimSpace(b.String()) {
		if g.tokens == nil || g.tokens[r] {
			out = append(out, r)
		}
	}
	return out
}

func (g *g2p) knows(s string) bool {
	if g.tokens == nil {
		return true
	}
	for _, r := range s {
		if !g.tokens[r] {
			return false
		}
	}
	return s != ""
}

// lookup finds w in the lexicons.
func (g *g2p) lookup(w string) (string, bool) {
	lw := strings.ToLower(w)
	for _, l := range g.primary {
		if p, ok := l[lw]; ok {
			return p, true
		}
	}
	if p, ok := g.fallback[lw]; ok {
		return g.convert(p), true
	}
	return "", false
}

// word returns the phonemes of one word. first is true for the first word
// of the sentence (where "A" is the article, not the letter).
func (g *g2p) word(w string, first bool) string {
	if w == "a" || w == "A" && first {
		return g.article
	}
	letters := []rune(w)
	if caps := isAllCaps(w); caps && len(letters) <= 3 && len(letters) > 1 {
		return g.spell(w)
	}
	if p, ok := g.lookup(w); ok {
		return p
	}
	lw := strings.ToLower(w)
	// Possessives and plurals of known words.
	for _, suf := range []string{"'s", "s'", "'"} {
		if stem, ok := strings.CutSuffix(lw, suf); ok && stem != "" {
			if p, ok := g.lookup(stem); ok {
				if suf == "'" {
					return p
				}
				return p + g.convert(sSuffix(p))
			}
		}
	}
	if strings.ContainsRune(lw, '\'') {
		if p, ok := g.lookup(strings.ReplaceAll(lw, "'", "")); ok {
			return p
		}
	}
	if p, ok := g.inflected(lw); ok {
		return p
	}
	// CamelCase and compounds: say the known parts.
	if parts := camelParts(w); len(parts) > 1 {
		var ps []string
		for _, part := range parts {
			ps = append(ps, g.word(part, false))
		}
		return strings.Join(ps, " ")
	}
	if p, ok := g.compound(lw); ok {
		return p
	}
	return g.spell(w)
}

// inflected says a regular inflection of a known word: plurals and third
// person (-s, -ies), past tenses (-ed, -ied), -ing and -ly.
func (g *g2p) inflected(w string) (string, bool) {
	try := func(stems ...string) (string, bool) {
		for _, s := range stems {
			if len(s) < 2 {
				continue
			}
			if p, ok := g.lookup(s); ok {
				return p, true
			}
		}
		return "", false
	}
	undouble := func(s string) string {
		if n := len(s); n > 2 && s[n-1] == s[n-2] {
			return s[:n-1]
		}
		return s
	}
	switch {
	case strings.HasSuffix(w, "ies"):
		if p, ok := try(strings.TrimSuffix(w, "ies") + "y"); ok {
			return p + "z", true
		}
	case strings.HasSuffix(w, "ied"):
		if p, ok := try(strings.TrimSuffix(w, "ied") + "y"); ok {
			return p + "d", true
		}
	case strings.HasSuffix(w, "ed"):
		s := strings.TrimSuffix(w, "ed")
		if p, ok := try(s, s+"e", undouble(s)); ok {
			return p + g.convert(edSuffix(p)), true
		}
	case strings.HasSuffix(w, "ing"):
		s := strings.TrimSuffix(w, "ing")
		if p, ok := try(s, s+"e", undouble(s)); ok {
			return p + "ɪŋ", true
		}
	case strings.HasSuffix(w, "ly"):
		if p, ok := try(strings.TrimSuffix(w, "ly")); ok {
			return p + "li", true
		}
	case strings.HasSuffix(w, "es"):
		if p, ok := try(strings.TrimSuffix(w, "es"), strings.TrimSuffix(w, "s")); ok {
			return p + g.convert(sSuffix(p)), true
		}
	case strings.HasSuffix(w, "s"):
		if p, ok := try(strings.TrimSuffix(w, "s")); ok {
			return p + g.convert(sSuffix(p)), true
		}
	}
	return "", false
}

// edSuffix is the espeak IPA of a past tense "-ed" after phonemes p.
func edSuffix(p string) string {
	switch lastPhone(p) {
	case 't', 'd', 'T':
		return "ɪd"
	case 'p', 'k', 'f', 's', 'ʃ', 'ʧ', 'θ':
		return "t"
	}
	return "d"
}

func lastPhone(p string) rune {
	rs := []rune(p)
	for i := len(rs) - 1; i >= 0; i-- {
		if r := rs[i]; r != 'ˈ' && r != 'ˌ' && r != 'ː' {
			return r
		}
	}
	return 0
}

// compound splits a word into two or three known words of at least three
// letters each, longest first part first.
func (g *g2p) compound(w string) (string, bool) {
	rs := []rune(w)
	for i := len(rs) - 3; i >= 3; i-- {
		a, b := string(rs[:i]), string(rs[i:])
		pa, ok := g.lookup(a)
		if !ok {
			continue
		}
		if pb, ok := g.lookup(b); ok {
			return pa + " " + pb, true
		}
		if len(rs)-i >= 6 {
			if pb, ok := g.compound(b); ok {
				return pa + " " + pb, true
			}
		}
	}
	return "", false
}

// spell says each letter of w; digits and other characters are skipped (the
// text front end has already spelled numbers out).
func (g *g2p) spell(w string) string {
	var ps []string
	for _, r := range strings.ToLower(w) {
		name, ok := letterNames[r]
		if !ok {
			continue
		}
		ps = append(ps, g.convert(name))
	}
	return strings.Join(ps, " ")
}

// letterNames are the names of the letters in espeak IPA.
var letterNames = map[rune]string{
	'a': "ˈeɪ", 'b': "bˈiː", 'c': "sˈiː", 'd': "dˈiː", 'e': "ˈiː", 'f': "ˈɛf", 'g': "dʒˈiː",
	'h': "ˈeɪtʃ", 'i': "ˈaɪ", 'j': "dʒˈeɪ", 'k': "kˈeɪ", 'l': "ˈɛl", 'm': "ˈɛm", 'n': "ˈɛn",
	'o': "ˈoʊ", 'p': "pˈiː", 'q': "kjˈuː", 'r': "ˈɑːɹ", 's': "ˈɛs", 't': "tˈiː", 'u': "jˈuː",
	'v': "vˈiː", 'w': "dˈʌbəljuː", 'x': "ˈɛks", 'y': "wˈaɪ", 'z': "zˈiː",
}

// sSuffix is the espeak IPA of a plural or possessive "s" after phonemes p.
func sSuffix(p string) string {
	switch lastPhone(p) {
	case 's', 'z', 'ʃ', 'ʒ', 'ʧ', 'ʤ':
		return "ɪz"
	case 'p', 't', 'k', 'f', 'θ', 'T':
		return "s"
	}
	return "z"
}

func isAllCaps(w string) bool {
	n := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			n++
		}
	}
	return n > 0
}

// camelParts splits "OpenAI" into "Open" and "AI", and "iPhone" into "i"
// and "Phone". A word with no inner capital is one part.
func camelParts(w string) []string {
	rs := []rune(w)
	var parts []string
	start := 0
	for i := 1; i < len(rs); i++ {
		lowerToUpper := unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i])
		acronymEnd := i+1 < len(rs) && unicode.IsUpper(rs[i-1]) && unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i+1])
		if lowerToUpper || acronymEnd {
			parts = append(parts, string(rs[start:i]))
			start = i
		}
	}
	return append(parts, string(rs[start:]))
}

type token struct {
	text  string
	punct bool
}

// tokenize splits a sentence into words (letters, digits and inner
// apostrophes) and punctuation. Hyphens and other symbols separate words.
func tokenize(s string) []token {
	var out []token
	var w []rune
	rs := []rune(s)
	flush := func() {
		if len(w) > 0 {
			out = append(out, token{text: strings.Trim(string(w), "'")})
			if out[len(out)-1].text == "" {
				out = out[:len(out)-1]
			}
			w = w[:0]
		}
	}
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			w = append(w, r)
		case r == '\'' && len(w) > 0 && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			w = append(w, r)
		case r == '\'' && len(w) > 0 && (w[len(w)-1] == 's' || w[len(w)-1] == 'S'):
			w = append(w, r) // plural possessive: "the butlers' pantry"
		case strings.ContainsRune(",.!?;:—…\"()", r):
			flush()
			out = append(out, token{text: string(r), punct: true})
		default:
			flush()
		}
	}
	flush()
	return out
}

// espeakToMisaki converts espeak-ng IPA (as in lexicon-en-us.txt) to the
// misaki alphabet Kokoro was trained on, after misaki's own espeak fallback.
// The source lexicon is American; for British voices the r after a vowel is
// dropped and the vowels are moved to their British forms. It is used only
// for words missing from Kokoro's own lexicons, so close is good enough.
func espeakToMisaki(s string, acc accent) string {
	common := []string{
		"aɪ", "I", "aʊ", "W", "dʒ", "ʤ", "tʃ", "ʧ", "eɪ", "A", "ɔɪ", "Y",
		"ɐ", "ə", "ᵻ", "ɪ", "r", "ɹ", "x", "k", "ç", "k", "ɬ", "l", "ʔ", "t", "ɫ", "l", "ʲ", "",
	}
	if acc == british {
		s = strings.NewReplacer(
			"oːɹ", "ɔː", "ɔːɹ", "ɔː", "ɑːɹ", "ɑː", "ɪɹ", "ɪə", "ʊɹ", "ʊə", "ɛɹ", "ɛː", "eɹ", "ɛː",
			"ɚ", "ə", "ɝ", "ɜː", "ɜːɹ", "ɜː", "oʊ", "Q", "əʊ", "Q", "ɾ", "t", "oː", "ɔː",
		).Replace(s)
		s = dropRhoticR(s)
	} else {
		s = strings.NewReplacer(
			"oʊ", "O", "əʊ", "O", "ɚ", "əɹ", "ɝ", "ɜɹ", "ɜːɹ", "ɜɹ", "ɜː", "ɜɹ", "ɾ", "T",
		).Replace(s)
	}
	s = strings.NewReplacer(common...).Replace(s)
	if acc != british {
		s = strings.ReplaceAll(s, "ː", "")
	}
	return s
}

// dropRhoticR removes an ɹ that follows a vowel and is not followed by one:
// "ˈæʃfɚd" has become "ˈæʃfəd" already; this catches "kˈɑɹt" style
// leftovers.
func dropRhoticR(s string) string {
	rs := []rune(s)
	var out []rune
	for i, r := range rs {
		if r == 'ɹ' && i > 0 && isVowelish(rs[i-1]) && (i+1 == len(rs) || !isVowelish(nextPhone(rs[i+1:]))) {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func nextPhone(rs []rune) rune {
	for _, r := range rs {
		if r != 'ˈ' && r != 'ˌ' {
			return r
		}
	}
	return 0
}

func isVowelish(r rune) bool {
	return strings.ContainsRune("aeiouæɑɒɔəɛɜɪʊʌɐːAIOQWY", r)
}
