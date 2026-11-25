package main

import (
	"strings"
	"unicode"
)

// normalize lower-cases, turns hyphens into spaces and drops everything except
// letters, numerals and apostrophes, so "Mr. Smith's well-known" and
// "MR SMITH'S WELL KNOWN" compare equal. Spelled-out versus numeral forms are
// not reconciled; LibriSpeech references spell numbers out, so a Whisper "3"
// counts as an error. That is a known, small bias, the same for all engines.
func normalize(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '\'':
			b.WriteRune(r)
		case r == '’':
			b.WriteRune('\'')
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

// werCounts returns the word-level edit distance between ref and hyp and the
// number of reference words.
func werCounts(ref, hyp string) (errs, refWords int) {
	r, h := normalize(ref), normalize(hyp)
	prev := make([]int, len(h)+1)
	cur := make([]int, len(h)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(r); i++ {
		cur[0] = i
		for j := 1; j <= len(h); j++ {
			c := prev[j-1]
			if r[i-1] != h[j-1] {
				c++
			}
			if prev[j]+1 < c {
				c = prev[j] + 1
			}
			if cur[j-1]+1 < c {
				c = cur[j-1] + 1
			}
			cur[j] = c
		}
		prev, cur = cur, prev
	}
	return prev[len(h)], len(r)
}

func wer(ref, hyp string) float64 {
	e, n := werCounts(ref, hyp)
	if n == 0 {
		if e == 0 {
			return 0
		}
		return 1
	}
	return float64(e) / float64(n)
}
