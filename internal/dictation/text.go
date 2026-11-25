package dictation

import (
	"strings"
	"unicode"
)

// Norm lower-cases a word and trims punctuation from both ends, so "Um," and
// "um" compare equal.
func Norm(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }))
}

// NormWords splits text into normalized words, dropping any that were only
// punctuation.
func NormWords(text string) []string {
	var out []string
	for _, w := range strings.Fields(text) {
		if n := Norm(w); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// HasPrefixWords reports whether the normalized words of tokens start with
// phrase (already normalized and split).
func HasPrefixWords(tokens, phrase []string) bool {
	if len(phrase) == 0 || len(tokens) < len(phrase) {
		return false
	}
	for i, p := range phrase {
		if Norm(tokens[i]) != p {
			return false
		}
	}
	return true
}

// trailingPunct returns the punctuation at the end of w.
func trailingPunct(w string) string {
	i := len(w)
	for i > 0 {
		r := rune(w[i-1])
		if r >= 0x80 || !unicode.IsPunct(r) {
			break
		}
		i--
	}
	return w[i:]
}
