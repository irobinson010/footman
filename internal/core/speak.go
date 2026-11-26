package core

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/speech"
)

// Reply policy values for Config.Speak (config replies.speak).
const (
	SpeakAlways    = "always"
	SpeakWhenVoice = "when-voice"
	SpeakNever     = "never"
)

// maxSentence is the length, in characters, past which a sentence with no
// end is split at a clause.
const maxSentence = 200

// SpeechStart is published when the first audio of a reply reaches the
// player.
type SpeechStart struct {
	Turn uint64
	// After is the time from the start of the command to that audio.
	After time.Duration
}

// SpeechStarted reports the time to first spoken audio for a reply.
var SpeechStarted = bus.NewTopic[SpeechStart]("pipeline.speech")

// SetReplyNotify turns showing spoken replies as notifications too on or off
// while running (replies.notify). A reply that is not spoken is shown either
// way.
func (p *Pipeline) SetReplyNotify(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Notify = on
}

func (p *Pipeline) notifyReplies() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Notify
}

// replyTitle is the title of reply notifications: the persona's name.
func (p *Pipeline) replyTitle() string {
	if n := p.Persona().Name; n != "" {
		return n
	}
	return "Footman"
}

// SetReplyPolicy changes, while running, when replies are spoken (speak is
// "always", "when-voice" or "never"; empty means "when-voice") and the quiet
// hours range like "22:00-07:00" (empty means none).
func (p *Pipeline) SetReplyPolicy(speak, quietHours string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Speak, p.cfg.QuietHours = speak, quietHours
}

// shouldSpeak applies the reply policy: quiet mode, replies.speak and
// replies.quiet_hours. typed is true for a request that did not come by voice.
func (p *Pipeline) shouldSpeak(typed bool) bool {
	p.mu.Lock()
	speak, hours, quiet := p.cfg.Speak, p.cfg.QuietHours, p.quiet
	p.mu.Unlock()
	if p.d.TTS == nil || quiet {
		return false
	}
	switch speak {
	case SpeakNever:
		return false
	case SpeakAlways:
	default: // when-voice
		if typed {
			return false
		}
	}
	now := time.Now
	if p.cfg.Now != nil {
		now = p.cfg.Now
	}
	return !InQuietHours(hours, now())
}

// InQuietHours reports whether t falls in the range spec, "HH:MM-HH:MM" in
// local 24-hour time; a range may cross midnight ("22:00-07:00"). An empty or
// malformed spec is never quiet.
func InQuietHours(spec string, t time.Time) bool {
	a, b, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return false
	}
	from, ok1 := clockMinutes(a)
	to, ok2 := clockMinutes(b)
	if !ok1 || !ok2 || from == to {
		return false
	}
	now := t.Hour()*60 + t.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to
}

func clockMinutes(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || len(h) == 0 || len(h) > 2 || len(m) != 2 {
		return 0, false
	}
	hh, mm := 0, 0
	for _, c := range h {
		if c < '0' || c > '9' {
			return 0, false
		}
		hh = hh*10 + int(c-'0')
	}
	for _, c := range m {
		if c < '0' || c > '9' {
			return 0, false
		}
		mm = mm*10 + int(c-'0')
	}
	if hh > 23 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// ---- sentence splitter ----

// Per-language rules (English now), like the dictation phrase lists.
type splitRules struct {
	// never are abbreviations that never end a sentence.
	never map[string]bool
	// soft are abbreviations that end a sentence only when an uppercase word
	// follows.
	soft map[string]bool
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var splitRulesByLang = map[string]splitRules{
	"en": {
		never: set("mr", "mrs", "ms", "dr", "prof", "sr", "jr", "st", "mt", "e.g", "i.e", "vs", "no", "fig", "approx", "dept", "gen", "col", "lt", "capt", "sgt", "rev", "hon"),
		soft:  set("etc", "a.m", "p.m", "inc", "ltd", "co", "corp", "u.s", "u.k"),
	},
}

func rulesFor(lang string) splitRules {
	if r, ok := splitRulesByLang[lang]; ok {
		return r
	}
	return splitRulesByLang["en"]
}

// Splitter cuts streamed text into sentences. Push it text as it arrives; it
// returns the sentences that are finished. A fenced code block comes out as
// one chunk starting with "```".
type Splitter struct {
	rules splitRules
	buf   string
}

// NewSplitter returns a Splitter with the rules of language lang ("" is
// English).
func NewSplitter(lang string) *Splitter { return &Splitter{rules: rulesFor(lang)} }

// Push adds text and returns the sentences it completed.
func (s *Splitter) Push(text string) []string {
	s.buf += text
	return s.drain(false)
}

// Flush returns what is left, as the last sentence.
func (s *Splitter) Flush() []string { return s.drain(true) }

func (s *Splitter) drain(final bool) []string {
	var out []string
	for s.buf != "" {
		end, ok := s.next(final)
		if !ok {
			if !final {
				break
			}
			end = len(s.buf) // what is left is the last sentence
		}
		chunk := strings.TrimSpace(s.buf[:end])
		s.buf = s.buf[end:]
		if chunk != "" {
			out = append(out, chunk)
		}
	}
	if final {
		s.buf = ""
	}
	return out
}

// next finds the end of the first chunk in the buffer.
func (s *Splitter) next(final bool) (int, bool) {
	b := s.buf
	if t := strings.TrimLeft(b, " \t\r\n"); strings.HasPrefix(t, "```") {
		off := len(b) - len(t)
		// the fence closes on a later line that starts with ```
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			rest := t[nl+1:]
			pos := 0
			for {
				line := rest[pos:]
				e := strings.IndexByte(line, '\n')
				if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
					if e < 0 {
						if final {
							return len(b), true
						}
						break // closing line may still be growing
					}
					return off + nl + 1 + pos + e + 1, true
				}
				if e < 0 {
					break
				}
				pos += e + 1
			}
		}
		if final {
			return len(b), true
		}
		return 0, false
	}
	runes := []rune(b)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\n' {
			return len(string(runes[:i+1])), true
		}
		if c != '.' && c != '!' && c != '?' && c != '…' {
			continue
		}
		j := i
		for j < len(runes) && strings.ContainsRune(".!?…", runes[j]) {
			j++
		}
		for j < len(runes) && strings.ContainsRune(`"')]}”’»`, runes[j]) {
			j++
		}
		if j == len(runes) {
			if final {
				return len(b), true
			}
			return s.clauseCap(runes)
		}
		if !unicode.IsSpace(runes[j]) {
			i = j - 1 // "3.5", "example.com", "ok.)x"
			continue
		}
		k := j
		for k < len(runes) && (runes[k] == ' ' || runes[k] == '\t' || runes[k] == '\r') {
			k++
		}
		// Does this full stop really end the sentence?
		if c == '.' || c == '…' {
			next, known := rune(0), k < len(runes)
			if known {
				next = runes[k]
			}
			if s.continues(runes[:i+1], runes[i:j], next, known) {
				if !known && !final {
					return s.clauseCap(runes)
				}
				i = j - 1
				continue
			}
			if !known && !final && s.needsNext(runes[:i+1], runes[i:j]) {
				return s.clauseCap(runes)
			}
		}
		return len(string(runes[:k])), true
	}
	return s.clauseCap(runes)
}

// needsNext reports whether the decision at this stop depends on the word
// after it.
func (s *Splitter) needsNext(upto, stops []rune) bool {
	if len(stops) > 1 || stops[0] == '…' {
		return true
	}
	w := lastToken(upto)
	return s.rules.soft[w]
}

// continues reports whether the stop(s) at the end of upto do not end a
// sentence. next is the first non-space rune after them, if known.
func (s *Splitter) continues(upto, stops []rune, next rune, known bool) bool {
	if len(stops) > 1 || stops[0] == '…' { // ellipsis
		return known && !unicode.IsUpper(next)
	}
	w := lastToken(upto)
	if w == "" {
		return false
	}
	if s.rules.never[w] {
		return true
	}
	if utf8.RuneCountInString(w) == 1 && unicode.IsLetter([]rune(w)[0]) {
		return true // an initial: "J. R. R. Tolkien"
	}
	if isDigits(w) && startsLine(upto, w) {
		return true // a list number: "1. Eggs"
	}
	if s.rules.soft[w] {
		return known && !unicode.IsUpper(next)
	}
	return false
}

func isDigits(w string) bool {
	for _, c := range w {
		if c < '0' || c > '9' {
			return false
		}
	}
	return w != ""
}

// startsLine reports whether the token is the first thing in upto.
func startsLine(upto []rune, tok string) bool {
	return strings.TrimSpace(string(upto)) == tok+"."
}

// lastToken returns the lower-cased word ending upto (which ends in a stop),
// keeping inner dots so "e.g" and "p.m" are one token.
func lastToken(upto []rune) string {
	end := len(upto) - 1 // drop the final stop
	for end > 0 && (upto[end] == '.' || upto[end] == '…') {
		end--
	}
	if upto[end] == '.' || upto[end] == '…' {
		return ""
	}
	start := end
	for start > 0 && (unicode.IsLetter(upto[start-1]) || unicode.IsDigit(upto[start-1]) || upto[start-1] == '.') {
		start--
	}
	return strings.ToLower(string(upto[start : end+1]))
}

// clauseCap splits a long unfinished sentence at its last clause break.
func (s *Splitter) clauseCap(runes []rune) (int, bool) {
	if len(runes) <= maxSentence {
		return 0, false
	}
	head := runes[:maxSentence]
	cut := -1
	for i := len(head) - 1; i > maxSentence/3; i-- {
		if strings.ContainsRune(",;:—–", head[i]) && (i+1 >= len(runes) || unicode.IsSpace(runes[i+1])) {
			cut = i + 1
			break
		}
	}
	if cut < 0 {
		for i := len(head) - 1; i > maxSentence/3; i-- {
			if unicode.IsSpace(head[i]) {
				cut = i + 1
				break
			}
		}
	}
	if cut < 0 {
		cut = maxSentence
	}
	return len(string(runes[:cut])), true
}

// ---- speakable text ----

var (
	reImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	reURL     = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>)\]]+`)
	reTag     = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	reInline  = regexp.MustCompile("`([^`]*)`")
	reBold    = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
	reItalic  = regexp.MustCompile(`(^|[\s(])[*_]([^*_\s][^*_]*?)[*_]([\s.,;:!?)]|$)`)
	reHead    = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+`)
	reBullet  = regexp.MustCompile(`(?m)^\s*(?:[-*+•]|\d{1,3}[.)])\s+`)
	reQuote   = regexp.MustCompile(`(?m)^\s*>+\s?`)
	reRule    = regexp.MustCompile(`(?m)^\s*(?:[-*_]\s*){3,}$`)
	reTableSp = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	reSpace   = regexp.MustCompile(`\s+`)
)

// Speakable turns a chunk of Markdown into text that reads well aloud: code
// blocks are dropped, links keep their text, bare URLs become "a link", and
// headings, lists, emphasis and tables lose their marks.
func Speakable(s string) string {
	if t := strings.TrimLeft(s, " \t\r\n"); strings.HasPrefix(t, "```") {
		return ""
	}
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reURL.ReplaceAllString(s, "a link")
	s = reTag.ReplaceAllString(s, "")
	s = reRule.ReplaceAllString(s, "")
	s = reTableSp.ReplaceAllString(s, "")
	s = reHead.ReplaceAllString(s, "")
	s = reBullet.ReplaceAllString(s, "")
	s = reQuote.ReplaceAllString(s, "")
	s = reInline.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$2")
	s = reItalic.ReplaceAllString(s, "$1$2$3")
	s = strings.NewReplacer("|", ", ", " ", " ").Replace(s)
	s = reSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(strings.Trim(s, ", "))
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return s
}

// mostlyCode reports whether so little of a reply is speakable that it is
// better shown than spoken.
func mostlyCode(raw, spoken int) bool {
	return raw >= 80 && spoken*100 < raw*40
}

const onScreenLine = "That is mostly code, so I have put it on screen."

// ---- speaking a reply ----

type speakerKey struct{}

func withSpeaker(ctx context.Context, s *speaker) context.Context {
	return context.WithValue(ctx, speakerKey{}, s)
}

func speakerFrom(ctx context.Context) *speaker {
	s, _ := ctx.Value(speakerKey{}).(*speaker)
	return s
}

// typedKey marks a context whose request came as text, not by voice.
type typedKey struct{}

// speaker speaks a reply sentence by sentence while it is still arriving:
// sentence n+1 is synthesized while sentence n plays, and the audio of all of
// them goes to one player stream, so there are no gaps between them.
type speaker struct {
	p      *Pipeline
	turn   uint64
	ctx    context.Context
	cancel context.CancelFunc
	start  time.Time

	split     *Splitter
	sentences chan string
	streams   chan speech.AudioStream
	synthDone chan error
	playDone  chan error

	closeIn     sync.Once
	raw, spoken int
	text        strings.Builder
	// streamedFinal is set when the whole final reply came through Feed.
	streamedFinal bool
	// shown is set when the whole reply is shown as a notification.
	shown bool
}

func (p *Pipeline) newSpeaker(ctx context.Context, turn uint64) *speaker {
	lang := p.cfg.Language
	sctx, cancel := context.WithCancel(ctx)
	s := &speaker{
		p: p, turn: turn, ctx: sctx, cancel: cancel, start: time.Now(),
		split:     NewSplitter(lang),
		sentences: make(chan string, 32),
		streams:   make(chan speech.AudioStream, 1),
		synthDone: make(chan error, 1),
		playDone:  make(chan error, 1),
	}
	go s.synth()
	go s.play()
	return s
}

// synth turns sentences into audio streams, one sentence ahead of playback.
func (s *speaker) synth() {
	var err error
	defer func() {
		close(s.streams)
		s.synthDone <- err
	}()
	for t := range s.sentences {
		if err != nil || s.ctx.Err() != nil {
			continue // drain
		}
		st, e := s.p.d.TTS.Speak(s.ctx, t, s.p.Persona().Voice)
		if e != nil {
			err = e
			s.cancel()
			continue
		}
		select {
		case s.streams <- st:
		case <-s.ctx.Done():
			_ = st.Close()
		}
	}
}

func (s *speaker) play() {
	first, ok := <-s.streams
	if !ok {
		s.playDone <- nil
		return
	}
	js := &joinStream{ctx: s.ctx, cur: first, rest: s.streams, onFirst: func() {
		bus.Publish(s.p.d.Bus, SpeechStarted, SpeechStart{Turn: s.turn, After: time.Since(s.start)})
	}}
	err := s.p.d.Platform.Audio().Play(s.ctx, s.p.cfg.Speaker, js)
	if err != nil {
		s.cancel()
	}
	_ = js.Close()
	s.playDone <- err
}

// Feed adds reply text as it arrives.
func (s *speaker) Feed(delta string) {
	s.text.WriteString(delta)
	s.emit(s.split.Push(delta))
}

// Break ends the sentence in progress, as at the end of a model turn.
func (s *speaker) Break() { s.emit(s.split.Flush()) }

func (s *speaker) emit(chunks []string) {
	for _, c := range chunks {
		t := Speakable(c)
		s.raw += utf8.RuneCountInString(c)
		s.spoken += utf8.RuneCountInString(t)
		if t == "" {
			continue
		}
		select {
		case s.sentences <- t:
		case <-s.ctx.Done():
		}
	}
}

// Finish speaks what is left and waits for the audio to end. A reply that was
// mostly code or links is also shown as a notification.
func (s *speaker) Finish() error {
	s.Break()
	if mostlyCode(s.raw, s.spoken) {
		select {
		case s.sentences <- onScreenLine:
		case <-s.ctx.Done():
		}
		if !s.shown {
			_, _ = s.p.d.Platform.Notifier().Notify(s.p.ctx, platform.Notification{Title: s.p.replyTitle(), Body: strings.TrimSpace(s.text.String())})
		}
	}
	s.closeIn.Do(func() { close(s.sentences) })
	synthErr, playErr := s.wait()
	s.cancel()
	switch {
	case playErr != nil && !errors.Is(playErr, context.Canceled):
		return wrapSpeech("play", playErr)
	case synthErr != nil:
		return wrapSpeech("speak", synthErr)
	case playErr != nil:
		return playErr
	}
	return nil
}

// Abort stops the speech at once and drops the rest of the reply.
func (s *speaker) Abort() {
	s.cancel()
	s.closeIn.Do(func() { close(s.sentences) })
	_, _ = s.wait()
}

// wait returns the synthesis and playback results once both have ended.
func (s *speaker) wait() (synth, play error) {
	return <-s.synthDone, <-s.playDone
}

type speechError struct {
	op  string
	err error
}

func wrapSpeech(op string, err error) error { return &speechError{op, err} }
func (e *speechError) Error() string        { return e.op + ": " + e.err.Error() }
func (e *speechError) Unwrap() error        { return e.err }

// joinStream plays the streams of successive sentences as one.
type joinStream struct {
	ctx     context.Context
	mu      sync.Mutex
	cur     speech.AudioStream
	rest    <-chan speech.AudioStream
	format  audio.Format
	started bool
	onFirst func()
	closed  bool
}

func (j *joinStream) Format() audio.Format {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur != nil {
		return j.cur.Format()
	}
	return j.format
}

func (j *joinStream) Recv() (audio.Frame, error) {
	for {
		j.mu.Lock()
		cur := j.cur
		j.mu.Unlock()
		if cur == nil {
			select {
			case next, ok := <-j.rest:
				if !ok {
					return audio.Frame{}, io.EOF
				}
				j.mu.Lock()
				j.cur = next
				j.mu.Unlock()
			case <-j.ctx.Done():
				return audio.Frame{}, j.ctx.Err()
			}
			continue
		}
		f, err := cur.Recv()
		if errors.Is(err, io.EOF) {
			j.mu.Lock()
			j.format = cur.Format()
			j.cur = nil
			j.mu.Unlock()
			_ = cur.Close()
			continue
		}
		if err == nil && !j.started {
			j.started = true
			j.onFirst()
		}
		return f, err
	}
}

func (j *joinStream) Close() error {
	j.mu.Lock()
	cur := j.cur
	j.cur, j.closed = nil, true
	j.mu.Unlock()
	if cur != nil {
		_ = cur.Close()
	}
	// Close the streams that were synthesized but never played.
	go func() {
		for st := range j.rest {
			_ = st.Close()
		}
	}()
	return nil
}
