package sherpa

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The text front end: it turns what an AI or a persona line says into plain
// words before the pronunciation lexicon sees it. Digits are the weak spot of
// small TTS models (spike S0-09), so numbers, times, dates, money, percentages,
// units and common abbreviations are spelled out here. English only.

var (
	reURL        = regexp.MustCompile(`\bhttps?://([^\s/?#]+)[^\s]*`)
	reEmail      = regexp.MustCompile(`\b([\w.+-]+)@([\w-]+(?:\.[\w-]+)+)\b`)
	reISODate    = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	reMoney      = regexp.MustCompile(`([$£€])\s?(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d{1,2}))?(?:\s?(thousand|million|billion|trillion|k|m|bn)\b)?`)
	reClock      = regexp.MustCompile(`(?i)\b(\d{1,2}):(\d{2})(?::\d{2})?(?:\s?([ap])\.?m\b\.?)?`)
	reHourAMPM   = regexp.MustCompile(`(?i)\b(\d{1,2})\s?([ap])\.?m\b\.?`)
	rePercent    = regexp.MustCompile(`(\d+(?:\.\d+)?)\s?%`)
	reOrdinal    = regexp.MustCompile(`(?i)\b(\d+)(st|nd|rd|th)\b`)
	reUnit       = regexp.MustCompile(`\b(\d+(?:\.\d+)?)\s?(km|kg|cm|mm|ms|mph|kph|MB|GB|TB|KB|kB|Mb|Gb|GHz|MHz|kHz|Hz|°C|°F)\b`)
	reDegrees    = regexp.MustCompile(`(\d)\s?°\s?([CF]\b)?`)
	reRange      = regexp.MustCompile(`\b(\d+)\s?[-–]\s?(\d+)\b`)
	reFraction   = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})\b`)
	reNumber     = regexp.MustCompile(`(^|[^\w.])(-)?(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d+))?`)
	reLetterNum  = regexp.MustCompile(`([A-Za-z])(\d)|(\d)([A-Za-z])`)
	reHash       = regexp.MustCompile(`#(\d)`)
	reNumAbbr    = regexp.MustCompile(`\bNo\.\s?(\d)`)
	reVersion    = regexp.MustCompile(`\b\d+(?:\.\d+){2,}\b`)
	reSpaces     = regexp.MustCompile(`[ \t]+`)
	reSpacePunct = regexp.MustCompile(` ([,.!?;:])`)
	reDigits     = regexp.MustCompile(`\d+`)
)

// abbreviations are expanded wherever they appear as a whole word, with the
// trailing period when they have one.
var abbreviations = []struct{ from, to string }{
	{"Mr.", "Mister"}, {"Mrs.", "Missus"}, {"Ms.", "Miz"}, {"Dr.", "Doctor"},
	{"Prof.", "Professor"}, {"Sr.", "Senior"}, {"Jr.", "Junior"}, {"Capt.", "Captain"},
	{"Lt.", "Lieutenant"}, {"Col.", "Colonel"}, {"Gen.", "General"}, {"Rev.", "Reverend"},
	{"St.", "Saint"}, {"Mt.", "Mount"}, {"Ave.", "Avenue"}, {"Rd.", "Road"},
	{"vs.", "versus"}, {"etc.", "et cetera"}, {"e.g.", "for example"}, {"i.e.", "that is"},
	{"approx.", "approximately"}, {"est.", "established"}, {"dept.", "department"},
	{"Ltd.", "Limited"}, {"Inc.", "Incorporated"}, {"Jan.", "January"}, {"Feb.", "February"}, {"Mar.", "March"},
	{"Apr.", "April"}, {"Jun.", "June"}, {"Jul.", "July"}, {"Aug.", "August"},
	{"Sep.", "September"}, {"Sept.", "September"}, {"Oct.", "October"}, {"Nov.", "November"},
	{"Dec.", "December"}, {"Mon.", "Monday"}, {"Tue.", "Tuesday"}, {"Wed.", "Wednesday"},
	{"Thu.", "Thursday"}, {"Fri.", "Friday"},
	{"vs", "versus"}, {"etc", "et cetera"}, {"OK", "okay"}, {"ok", "okay"},
}

// sentenceEnders are the abbreviations whose period can also end a sentence.
var sentenceEnders = map[string]bool{"etc.": true, "Ltd.": true, "Inc.": true}

// endsSentence reports whether the text after an abbreviation starts a new
// sentence: nothing, or a space and a capital letter.
func endsSentence(after string) bool {
	t := strings.TrimLeft(after, " ")
	return t == "" || strings.HasPrefix(t, "\n") || len(t) < len(after) && unicode.IsUpper(firstRune(t))
}

var unitWords = map[string][2]string{
	"km": {"kilometre", "kilometres"}, "kg": {"kilogram", "kilograms"},
	"cm": {"centimetre", "centimetres"}, "mm": {"millimetre", "millimetres"},
	"ms": {"millisecond", "milliseconds"}, "mph": {"mile per hour", "miles per hour"},
	"kph": {"kilometre per hour", "kilometres per hour"},
	"MB":  {"megabyte", "megabytes"}, "GB": {"gigabyte", "gigabytes"}, "TB": {"terabyte", "terabytes"},
	"KB": {"kilobyte", "kilobytes"}, "kB": {"kilobyte", "kilobytes"},
	"Mb": {"megabit", "megabits"}, "Gb": {"gigabit", "gigabits"},
	"GHz": {"gigahertz", "gigahertz"}, "MHz": {"megahertz", "megahertz"},
	"kHz": {"kilohertz", "kilohertz"}, "Hz": {"hertz", "hertz"},
	"°C": {"degree Celsius", "degrees Celsius"}, "°F": {"degree Fahrenheit", "degrees Fahrenheit"},
}

var currencies = map[string][4]string{ // singular, plural, minor singular, minor plural
	"$": {"dollar", "dollars", "cent", "cents"},
	"£": {"pound", "pounds", "penny", "pence"},
	"€": {"euro", "euros", "cent", "cents"},
}

var monthNames = []string{"", "January", "February", "March", "April", "May", "June", "July",
	"August", "September", "October", "November", "December"}

// NormalizeText spells out what a TTS model would misread: numbers, times,
// dates, money, percentages, units, symbols and common abbreviations. It
// keeps sentence punctuation, so the result can still be split into
// sentences. It is exported for the clip bank and tests.
func NormalizeText(s string) string {
	s = strings.NewReplacer(
		" ", " ", "‘", "'", "’", "'", "“", `"`, "”", `"`,
		"—", ", ", "–", "-", "…", "...", "\r", "",
		"**", "", "__", "", "`", "", "~", " ", "|", " ", "*", " ",
	).Replace(s)
	s = reURL.ReplaceAllStringFunc(s, func(m string) string {
		host := strings.TrimPrefix(reURL.FindStringSubmatch(m)[1], "www.")
		return strings.ReplaceAll(host, ".", " dot ")
	})
	s = reEmail.ReplaceAllStringFunc(s, func(m string) string {
		p := reEmail.FindStringSubmatch(m)
		return strings.ReplaceAll(p[1], ".", " dot ") + " at " + strings.ReplaceAll(p[2], ".", " dot ")
	})
	s = reISODate.ReplaceAllStringFunc(s, func(m string) string {
		p := reISODate.FindStringSubmatch(m)
		mo, _ := strconv.Atoi(p[2])
		d, _ := strconv.Atoi(p[3])
		if mo < 1 || mo > 12 || d < 1 || d > 31 {
			return m
		}
		return monthNames[mo] + " " + ordinalWords(d) + ", " + yearWords(p[1])
	})
	s = reMoney.ReplaceAllStringFunc(s, moneyWords)
	s = replaceKeepingStop(reClock, s, func(p []string) (string, bool) {
		h, _ := strconv.Atoi(p[1])
		mi, _ := strconv.Atoi(p[2])
		if h > 24 || mi > 59 {
			return "", false
		}
		return clockWords(h, mi, p[3]), true
	})
	s = replaceKeepingStop(reHourAMPM, s, func(p []string) (string, bool) {
		h, _ := strconv.Atoi(p[1])
		if h < 1 || h > 12 {
			return "", false
		}
		return cardinalWords(int64(h)) + " " + ampm(p[2]), true
	})
	s = reVersion.ReplaceAllStringFunc(s, func(m string) string {
		var parts []string
		for _, d := range strings.Split(m, ".") {
			parts = append(parts, numberWords(false, d, ""))
		}
		return strings.Join(parts, " point ")
	})
	s = reNumAbbr.ReplaceAllString(s, "number $1")
	s = reHash.ReplaceAllString(s, "number $1")
	s = rePercent.ReplaceAllString(s, "$1 percent")
	s = reOrdinal.ReplaceAllStringFunc(s, func(m string) string {
		p := reOrdinal.FindStringSubmatch(m)
		n, err := strconv.Atoi(p[1])
		if err != nil || n > 1_000_000 {
			return m
		}
		return ordinalWords(n)
	})
	s = reUnit.ReplaceAllStringFunc(s, func(m string) string {
		p := reUnit.FindStringSubmatch(m)
		w := unitWords[p[2]]
		if p[1] == "1" {
			return p[1] + " " + w[0]
		}
		return p[1] + " " + w[1]
	})
	s = reDegrees.ReplaceAllStringFunc(s, func(m string) string {
		p := reDegrees.FindStringSubmatch(m)
		out := p[1] + " degrees"
		switch p[2] {
		case "C":
			out += " Celsius"
		case "F":
			out += " Fahrenheit"
		}
		return out
	})
	s = reRange.ReplaceAllString(s, "$1 to $2")
	s = reFraction.ReplaceAllStringFunc(s, func(m string) string {
		p := reFraction.FindStringSubmatch(m)
		return fractionWords(p[1], p[2])
	})
	s = reLetterNum.ReplaceAllString(s, "$1$3 $2$4")
	s = reLetterNum.ReplaceAllString(s, "$1$3 $2$4") // overlapping pairs such as a1b
	s = expandAbbreviations(s)
	s = reNumber.ReplaceAllStringFunc(s, func(m string) string {
		p := reNumber.FindStringSubmatch(m)
		return p[1] + numberWords(p[2] != "", p[3], p[4])
	})
	// Digits left over (such as the parts of a version number) are read
	// one by one.
	s = reDigits.ReplaceAllStringFunc(s, func(m string) string { return " " + digitWords(m) + " " })
	s = strings.NewReplacer(
		"&", " and ", "@", " at ", "+", " plus ", "=", " equals ", "%", " percent",
		"°", " degrees", "#", " ", "<", " ", ">", " ", "[", " ", "]", " ", "{", " ", "}", " ",
		"_", " ", "^", " ", "\\", " ", "/", " ",
	).Replace(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = reSpaces.ReplaceAllString(l, " ")
		lines[i] = strings.TrimSpace(reSpacePunct.ReplaceAllString(l, "$1"))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// replaceKeepingStop replaces the matches of re (whose expansion may end in
// the full stop of "p.m.") with what fn returns, putting the full stop back
// when it also ended the sentence. fn returns false to leave a match alone.
func replaceKeepingStop(re *regexp.Regexp, s string, fn func(groups []string) (string, bool)) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		groups := make([]string, len(loc)/2)
		for i := range groups {
			if loc[2*i] >= 0 {
				groups[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		out, ok := fn(groups)
		if !ok {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(out)
		if strings.HasSuffix(groups[0], ".") && endsSentence(s[loc[1]:]) {
			b.WriteString(".")
		}
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func expandAbbreviations(s string) string {
	for _, a := range abbreviations {
		if !strings.Contains(s, a.from) {
			continue
		}
		var b strings.Builder
		rest := s
		for {
			i := strings.Index(rest, a.from)
			if i < 0 {
				b.WriteString(rest)
				break
			}
			end := i + len(a.from)
			before := i == 0 || !isWordRune(lastRune(rest[:i]))
			after := end == len(rest) || !isWordRune(firstRune(rest[end:]))
			if strings.HasSuffix(a.from, ".") {
				after = end == len(rest) || !isWordRune(firstRune(rest[end:])) && firstRune(rest[end:]) != '.'
			}
			b.WriteString(rest[:i])
			if before && after {
				b.WriteString(a.to)
				// "etc." at the end of a sentence keeps its full stop.
				if sentenceEnders[a.from] && endsSentence(rest[end:]) {
					b.WriteString(".")
				}
			} else {
				b.WriteString(a.from)
			}
			rest = rest[end:]
		}
		s = b.String()
	}
	return s
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func ampm(letter string) string {
	if strings.EqualFold(letter, "a") {
		return "a m"
	}
	return "p m"
}

func clockWords(h, m int, ap string) string {
	var out string
	switch {
	case m == 0 && ap == "":
		if h == 0 || h == 24 {
			return "midnight"
		}
		if h == 12 {
			return "twelve o'clock"
		}
		out = cardinalWords(int64(h)) + " o'clock"
		if h > 12 {
			out = cardinalWords(int64(h)) + " hundred hours"
		}
		return out
	case m == 0:
		out = cardinalWords(int64(h))
	case m < 10:
		out = cardinalWords(int64(h)) + " oh " + cardinalWords(int64(m))
	default:
		out = cardinalWords(int64(h)) + " " + cardinalWords(int64(m))
	}
	if ap != "" {
		out += " " + ampm(ap)
	}
	return out
}

func moneyWords(m string) string {
	p := reMoney.FindStringSubmatch(m)
	c := currencies[p[1]]
	whole, err := strconv.ParseInt(strings.ReplaceAll(p[2], ",", ""), 10, 64)
	if err != nil {
		return m
	}
	scale := map[string]string{"k": "thousand", "m": "million", "bn": "billion"}[strings.ToLower(p[4])]
	if scale == "" {
		scale = strings.ToLower(p[4])
	}
	if scale != "" {
		n := cardinalWords(whole)
		if p[3] != "" {
			n += " point " + digitWords(p[3])
		}
		return n + " " + scale + " " + c[1]
	}
	out := cardinalWords(whole) + " " + c[1]
	if whole == 1 {
		out = "one " + c[0]
	}
	if p[3] != "" {
		minor, _ := strconv.Atoi((p[3] + "0")[:2])
		if minor > 0 {
			unit := c[3]
			if minor == 1 {
				unit = c[2]
			}
			if whole == 0 {
				return cardinalWords(int64(minor)) + " " + unit
			}
			out += " and " + cardinalWords(int64(minor)) + " " + unit
		}
	}
	return out
}

func fractionWords(a, b string) string {
	n, _ := strconv.Atoi(a)
	d, _ := strconv.Atoi(b)
	if d == 0 {
		return a + " " + b
	}
	switch {
	case n == 1 && d == 2:
		return "one half"
	case d == 4 && n < 4:
		if n == 1 {
			return "one quarter"
		}
		return cardinalWords(int64(n)) + " quarters"
	case d > 2 && d <= 10 && n < d:
		w := ordinalWords(d)
		if n != 1 {
			w += "s"
		}
		return cardinalWords(int64(n)) + " " + w
	}
	return cardinalWords(int64(n)) + " over " + cardinalWords(int64(d))
}

// numberWords reads a number found in text: grouped thousands, a decimal
// part, years, and long or zero-padded digit strings (read digit by digit).
func numberWords(negative bool, whole, frac string) string {
	sign := ""
	if negative {
		sign = "minus "
	}
	plain := !strings.Contains(whole, ",")
	digits := strings.ReplaceAll(whole, ",", "")
	if plain && (len(digits) > 1 && digits[0] == '0' || len(digits) > 9) {
		out := sign + digitWords(digits)
		if frac != "" {
			out += " point " + digitWords(frac)
		}
		return out
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return sign + digitWords(digits)
	}
	if plain && frac == "" && !negative && len(digits) == 4 && n >= 1100 && n <= 2099 {
		return yearWords(digits)
	}
	out := sign + cardinalWords(n)
	if frac != "" {
		out += " point " + digitWords(frac)
	}
	return out
}

var smallNumbers = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen",
	"eighteen", "nineteen"}

var tens = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}

// cardinalWords spells n in words, British style ("one hundred and five").
func cardinalWords(n int64) string {
	if n < 0 {
		return "minus " + cardinalWords(-n)
	}
	if n < 20 {
		return smallNumbers[n]
	}
	if n < 100 {
		if n%10 == 0 {
			return tens[n/10]
		}
		return tens[n/10] + "-" + smallNumbers[n%10]
	}
	if n < 1000 {
		out := smallNumbers[n/100] + " hundred"
		if n%100 != 0 {
			out += " and " + cardinalWords(n%100)
		}
		return out
	}
	scales := []struct {
		v    int64
		name string
	}{{1e18, "quintillion"}, {1e15, "quadrillion"}, {1e12, "trillion"}, {1e9, "billion"}, {1e6, "million"}, {1e3, "thousand"}}
	for _, sc := range scales {
		if n >= sc.v {
			out := cardinalWords(n/sc.v) + " " + sc.name
			if r := n % sc.v; r != 0 {
				if r < 100 {
					out += " and " + cardinalWords(r)
				} else {
					out += " " + cardinalWords(r)
				}
			}
			return out
		}
	}
	return strconv.FormatInt(n, 10)
}

var ordinalIrregular = map[string]string{
	"one": "first", "two": "second", "three": "third", "five": "fifth", "eight": "eighth",
	"nine": "ninth", "twelve": "twelfth",
}

// ordinalWords spells n as an ordinal ("twenty-first").
func ordinalWords(n int) string {
	w := cardinalWords(int64(n))
	i := strings.LastIndexAny(w, " -")
	head, last := w[:i+1], w[i+1:]
	if o, ok := ordinalIrregular[last]; ok {
		return head + o
	}
	if strings.HasSuffix(last, "y") {
		return head + strings.TrimSuffix(last, "y") + "ieth"
	}
	return head + last + "th"
}

// yearWords reads a four-digit year: 1984 is "nineteen eighty-four", 2005 is
// "two thousand and five", 2026 is "twenty twenty-six".
func yearWords(y string) string {
	n, err := strconv.Atoi(y)
	if err != nil || len(y) != 4 {
		return y
	}
	hi, lo := n/100, n%100
	switch {
	case n%1000 < 10 && n >= 2000:
		return cardinalWords(int64(n))
	case lo == 0:
		return cardinalWords(int64(hi)) + " hundred"
	case lo < 10:
		return cardinalWords(int64(hi)) + " oh " + cardinalWords(int64(lo))
	}
	return cardinalWords(int64(hi)) + " " + cardinalWords(int64(lo))
}

// digitWords reads digits one by one ("oh" for zero).
func digitWords(d string) string {
	var w []string
	for _, r := range d {
		switch {
		case r == '0':
			w = append(w, "oh")
		case r > '0' && r <= '9':
			w = append(w, smallNumbers[r-'0'])
		}
	}
	return strings.Join(w, " ")
}

// splitSentences cuts normalized text into the pieces synthesized one at a
// time. A piece ends at . ! ? or a line break. Sentences longer than
// maxWords are cut again at a comma, semicolon or colon, and the first
// sentence is cut at its first such pause after firstWords words, so the
// first audio comes sooner (a negative firstWords never cuts it).
func splitSentences(s string, firstWords, maxWords int) []string {
	var out []string
	var cur strings.Builder
	rs := []rune(s)
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" && hasWord(t) {
			out = append(out, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\n' {
			flush()
			continue
		}
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			for i+1 < len(rs) && strings.ContainsRune(`.!?"')`, rs[i+1]) {
				i++
				cur.WriteRune(rs[i])
			}
			if i+1 == len(rs) || unicode.IsSpace(rs[i+1]) {
				flush()
			}
		}
	}
	flush()
	var pieces []string
	for i, p := range out {
		first := -1
		if i == 0 {
			first = firstWords
		}
		pieces = append(pieces, splitClauses(p, first, maxWords)...)
	}
	return pieces
}

// splitClauses cuts one sentence into pieces. With first >= 0 the first
// piece ends at the first clause pause (, ; :) after first words. A sentence
// longer than maxW words is cut at pauses into pieces of at least maxW/2
// words, and, where it has no pause, every maxW*3/2 words. No piece after a
// cut has fewer than three words.
func splitClauses(s string, first, maxW int) []string {
	words := strings.Fields(s)
	long := len(words) > maxW
	if first < 0 && !long {
		return []string{s}
	}
	var out []string
	start := 0
	for i, w := range words {
		n := i + 1 - start
		if i+1 == len(words) || len(words)-i-1 < 3 {
			break
		}
		pause := strings.ContainsAny(w[len(w)-1:], ",;:")
		cut := false
		switch {
		case len(out) == 0 && first >= 0 && n >= first && pause:
			cut = true
		case long && n >= maxW/2 && pause:
			cut = true
		case long && n >= maxW*3/2:
			cut = true
		}
		if cut {
			out = append(out, strings.Join(words[start:i+1], " "))
			start = i + 1
		}
	}
	return append(out, strings.Join(words[start:], " "))
}

func hasWord(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
