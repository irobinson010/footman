package sherpa

import (
	"sort"
	"strings"
	"unicode"
)

// respeller replaces words and phrases with an English respelling before
// grapheme-to-phoneme conversion (speech.Voice.Respell): names and foreign
// words the lexicons lack are otherwise spelled out letter by letter.
type respeller struct {
	// entries are the keys split into lower-case words, longest phrase
	// first, with their replacement text.
	entries []respellEntry
}

type respellEntry struct {
	words []string
	with  string
}

func newRespeller(table map[string]string) *respeller {
	var es []respellEntry
	for k, v := range table {
		if w := strings.Fields(strings.ToLower(k)); len(w) > 0 && strings.TrimSpace(v) != "" {
			es = append(es, respellEntry{words: w, with: v})
		}
	}
	if len(es) == 0 {
		return nil
	}
	sort.Slice(es, func(i, j int) bool {
		if len(es[i].words) != len(es[j].words) {
			return len(es[i].words) > len(es[j].words)
		}
		return strings.Join(es[i].words, " ") < strings.Join(es[j].words, " ")
	})
	return &respeller{entries: es}
}

func isRespellRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' }

// apply returns text with every whole-word, case-insensitive match replaced,
// longest phrase first. A phrase matches across any run of spaces.
func (r *respeller) apply(text string) string {
	if r == nil {
		return text
	}
	rs := []rune(text)
	var out strings.Builder
	for i := 0; i < len(rs); {
		if !isRespellRune(rs[i]) || (i > 0 && isRespellRune(rs[i-1])) {
			out.WriteRune(rs[i])
			i++
			continue
		}
		if end, with, ok := r.match(rs, i); ok {
			out.WriteString(with)
			i = end
			continue
		}
		out.WriteRune(rs[i])
		i++
	}
	return out.String()
}

// match tries each entry at the word starting at rs[i].
func (r *respeller) match(rs []rune, i int) (end int, with string, ok bool) {
	for _, e := range r.entries {
		j := i
		for n, w := range e.words {
			if n > 0 {
				k := j
				for k < len(rs) && unicode.IsSpace(rs[k]) {
					k++
				}
				if k == j {
					j = -1
					break
				}
				j = k
			}
			wr := []rune(w)
			if j+len(wr) > len(rs) || !strings.EqualFold(string(rs[j:j+len(wr)]), w) {
				j = -1
				break
			}
			j += len(wr)
		}
		if j < 0 || (j < len(rs) && isRespellRune(rs[j])) {
			continue
		}
		return j, e.with, true
	}
	return 0, "", false
}
