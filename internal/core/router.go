package core

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/irobinson010/footman/internal/dictation"
)

// Trigger is what started an utterance.
type Trigger int

// Triggers.
const (
	// TriggerWake is the wake word. What follows is a command, unless it
	// starts with "dictate" or "write".
	TriggerWake Trigger = iota + 1
	// TriggerDictate is the dictation hotkey: always dictation.
	TriggerDictate
	// TriggerCommand is the command hotkey, or text sent as a command through
	// the local API (`say.text`): always a command.
	TriggerCommand
)

var triggerNames = [...]string{TriggerWake: "wake", TriggerDictate: "dictate", TriggerCommand: "command"}

// String returns the trigger's lower-case name.
func (t Trigger) String() string {
	if t >= TriggerWake && t <= TriggerCommand {
		return triggerNames[t]
	}
	return fmt.Sprintf("Trigger(%d)", int(t))
}

// RouteKind is where the router sends an utterance.
type RouteKind int

// Route kinds.
const (
	// RouteNone drops the utterance: a wake word with nothing clear after it
	// (design §17, false wakes are dropped silently).
	RouteNone RouteKind = iota
	// RouteDictation cleans the text up and inserts it at the cursor.
	RouteDictation
	// RouteCommand acknowledges and acts on the text.
	RouteCommand
)

var routeNames = [...]string{RouteNone: "none", RouteDictation: "dictation", RouteCommand: "command"}

// String returns the route's lower-case name.
func (k RouteKind) String() string {
	if k >= RouteNone && k <= RouteCommand {
		return routeNames[k]
	}
	return fmt.Sprintf("RouteKind(%d)", int(k))
}

// Route is the router's decision.
type Route struct {
	// Kind says where the text goes.
	Kind RouteKind
	// Text is what is left to dictate or act on, with the wake word and the
	// "dictate" / "write" keyword removed.
	Text string
}

// Default phrase lists for the simple tier (English). Strings and phrase lists
// are per language (design §17); these are the English ones.
var (
	// DefaultDictateWords start a dictation after the wake word.
	DefaultDictateWords = []string{"dictate", "write"}
	// DefaultStopPhrases are the break-glass phrases (ADR-0017). They only
	// count after the wake word.
	DefaultStopPhrases = []string{"stop everything", "cancel everything"}
)

// Router is the simple-tier router (design §17): the wake word followed by
// "dictate" or "write" is dictation, anything else after the wake word is a
// command, and each hotkey means what it says. The smart tier will replace it
// behind the same Route method.
type Router struct {
	mu      sync.RWMutex // guards wake
	wake    [][]string
	dictate [][]string
	stop    [][]string
}

// NewRouter builds a router. wakeWords are stripped from the start of a
// transcript if the speech-to-text engine kept them; dictateWords and
// stopPhrases default to DefaultDictateWords and DefaultStopPhrases when nil.
func NewRouter(wakeWords, dictateWords, stopPhrases []string) *Router {
	if dictateWords == nil {
		dictateWords = DefaultDictateWords
	}
	if stopPhrases == nil {
		stopPhrases = DefaultStopPhrases
	}
	return &Router{wake: splitPhrases(wakeWords), dictate: splitPhrases(dictateWords), stop: splitPhrases(stopPhrases)}
}

func splitPhrases(ps []string) [][]string {
	var out [][]string
	for _, p := range ps {
		if w := dictation.NormWords(p); len(w) > 0 {
			out = append(out, w)
		}
	}
	return out
}

// SetWakeWords replaces the wake words stripped from transcripts, as when the
// listening personas change while running.
func (r *Router) SetWakeWords(wakeWords []string) {
	w := splitPhrases(wakeWords)
	r.mu.Lock()
	r.wake = w
	r.mu.Unlock()
}

// stripPrefix removes the first of phrases found at the start of tokens.
func stripPrefix(tokens []string, phrases [][]string) ([]string, bool) {
	for _, p := range phrases {
		if dictation.HasPrefixWords(tokens, p) {
			return tokens[len(p):], true
		}
	}
	return tokens, false
}

// stripWake removes a wake word from the start of tokens. The command audio
// starts a little before the end of the wake word (the spotter reports it
// late), so the transcript may begin with the wake word's tail instead, as in
// "ford, open YouTube" for "Ashford, open YouTube": a first word that is the
// last four or more letters of a wake word is dropped too.
func (r *Router) stripWake(tokens []string) []string {
	r.mu.RLock()
	wake := r.wake
	r.mu.RUnlock()
	if rest, ok := stripPrefix(tokens, wake); ok || len(tokens) == 0 {
		return rest
	}
	first := dictation.Norm(tokens[0])
	if len(first) < 4 {
		return tokens
	}
	for _, p := range wake {
		if last := p[len(p)-1]; len(last) > len(first) && strings.HasSuffix(last, first) {
			return tokens[1:]
		}
	}
	return tokens
}

// wakeFillers may come before the wake word in a transcript MatchWake
// accepts ("okay, Alfred, ..."), up to two of them. Other words may not: "the
// Alfred Hitchcock film" is talk about Alfred, not to him.
var wakeFillers = map[string]bool{
	"um": true, "umm": true, "uh": true, "uhh": true, "uhm": true, "er": true, "erm": true,
	"ah": true, "hmm": true, "mm": true, "oh": true, "okay": true, "ok": true, "so": true, "well": true,
}

// MatchWake is the second stage of the wake word (ADR-0041): it reports
// whether text, the transcript of the audio from just before the wake word,
// starts with one of the wake words, after at most two fillers such as "um"
// or "okay", and returns what follows it. The match is loose, for the spellings
// speech-to-text gives names: letters are compared with the spaces and
// punctuation removed ("Hey, Foot man"), three wrong letters in ten are allowed
// ("Ashferd"), or the consonants may sound the same ("Alford", "Esfort"),
// and a first word
// that is the last four or more letters of a wake word counts ("ford"), as
// in stripWake.
func (r *Router) MatchWake(text string) (rest string, ok bool) {
	r.mu.RLock()
	wake := r.wake // the live list (SetWakeWords)
	r.mu.RUnlock()
	tokens := strings.Fields(text)
	// Words that were only punctuation do not count towards the lead.
	var idx []int
	for i, t := range tokens {
		if letters(t) != "" {
			idx = append(idx, i)
		}
	}
	// A phrase's last word alone also counts when it is a name of four or
	// more letters, as speech-to-text often drops a leading "hey" ("Ashford"
	// for "hey ashford").
	phrases := wake
	for _, p := range wake {
		if len(p) > 1 && len(letters(p[len(p)-1])) >= 4 {
			phrases = append(phrases[:len(phrases):len(phrases)], p[len(p)-1:])
		}
	}
	for s := 0; s <= 2 && s < len(idx); s++ {
		if s > 0 && !wakeFillers[letters(tokens[idx[s-1]])] {
			break
		}
		// The closest match wins, so "Hey Foot man" takes all three words.
		best, end := -1, 0
		for _, p := range phrases {
			want := letters(strings.Join(p, ""))
			got := ""
			for k := 0; k <= len(p) && s+k < len(idx); k++ {
				got += letters(tokens[idx[s+k]])
				if d, ok := wakeLike(got, want); ok && (best < 0 || d < best) {
					best, end = d, idx[s+k]
				}
			}
		}
		if best >= 0 {
			return strings.Join(tokens[end+1:], " "), true
		}
		first := letters(tokens[idx[s]])
		if len(first) < 4 {
			continue
		}
		for _, p := range wake {
			if last := letters(p[len(p)-1]); len(last) > len(first) && strings.HasSuffix(last, first) {
				return strings.Join(tokens[idx[s]+1:], " "), true
			}
		}
	}
	return "", false
}

// letters returns the lower-case letters of w.
func letters(w string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(w) {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wakeLike reports whether got is close enough to the wake word want: at
// most three letters in ten different (edit distance), or consonants that
// sound the same (consonantCode) with about the same length. It also returns
// the edit distance, to pick the closest of several matches.
func wakeLike(got, want string) (int, bool) {
	if got == "" {
		return 0, false
	}
	d := editDistance(got, want)
	if d <= max(1, len(want)*3/10) {
		return d, true
	}
	n := len(got) - len(want)
	wc := consonantCode(want)
	return d, len(wc) >= 3 && consonantCode(got) == wc && n >= -2 && n <= 2
}

// soundClass groups consonants that speech-to-text mixes up, as Soundex
// does; vowels, h, w and y have none.
var soundClass = map[rune]byte{
	'b': '1', 'f': '1', 'p': '1', 'v': '1',
	'c': '2', 'g': '2', 'j': '2', 'k': '2', 'q': '2', 's': '2', 'x': '2', 'z': '2',
	'd': '3', 't': '3', 'l': '4', 'm': '5', 'n': '5', 'r': '6',
}

// consonantCode is the sound classes of w's consonants, repeats collapsed:
// "ashford" and "esfort" are both "2163", "alfred" and "alford" "4163".
func consonantCode(w string) string {
	var b []byte
	for _, r := range w {
		c, ok := soundClass[r]
		if !ok || (len(b) > 0 && b[len(b)-1] == c) {
			continue
		}
		b = append(b, c)
	}
	return string(b)
}

// editDistance is the Levenshtein distance between a and b, in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := prev[j-1]
			if ra[i-1] != rb[j-1] {
				c++
			}
			cur[j] = min(c, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// BreakGlass reports whether text is the break-glass stop phrase. It is only
// true for a wake-word utterance (ADR-0017): "stop everything" said to
// someone on a call, typed by dictation or sent as a hotkey command does
// nothing special.
func (r *Router) BreakGlass(t Trigger, text string) bool {
	if t != TriggerWake {
		return false
	}
	tokens := r.stripWake(strings.Fields(text))
	rest := dictation.NormWords(strings.Join(tokens, " "))
	for _, p := range r.stop {
		if len(rest) == len(p) && dictation.HasPrefixWords(rest, p) {
			return true
		}
	}
	return false
}

// Route decides where an utterance goes.
func (r *Router) Route(t Trigger, text string) Route {
	tokens := strings.Fields(text)
	switch t {
	case TriggerDictate:
		return route(RouteDictation, tokens)
	case TriggerCommand:
		return route(RouteCommand, tokens)
	}
	tokens = r.stripWake(tokens)
	if rest, ok := stripPrefix(tokens, r.dictate); ok {
		return route(RouteDictation, rest)
	}
	return route(RouteCommand, tokens)
}

func route(k RouteKind, tokens []string) Route {
	// Drop punctuation left over from a stripped keyword, as in
	// "Dictate: hello" or "Footman, open YouTube".
	for len(tokens) > 0 && dictation.Norm(tokens[0]) == "" {
		tokens = tokens[1:]
	}
	text := strings.Join(tokens, " ")
	if len(dictation.NormWords(text)) == 0 {
		return Route{Kind: RouteNone}
	}
	return Route{Kind: k, Text: text}
}
