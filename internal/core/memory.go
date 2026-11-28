package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/plugins"
)

// Short-term conversation memory (ADR-0052). The AI keeps the last few
// exchanges across wake-word commands, so "open those links" can refer to the
// answer before it. It lives in memory only: it is never written to disk or
// logged, and it is the short-term layer under the opt-in long-term memory of
// design §18, which is a separate feature.

// Defaults for ai.context.turns and ai.context.ttl.
const (
	DefaultContextTurns = 6
	DefaultContextTTL   = 10 * time.Minute
)

// maxNoteSources caps the pages listed in the note an old exchange keeps.
const maxNoteSources = 8

// maxNoteTitle caps a page title in that note, in characters.
const maxNoteTitle = 100

// exchange is one request and its reply: the user message, whatever tool
// calls and results came between, and the final assistant message.
type exchange struct {
	msgs []llm.Message
	// untrusted is true when this exchange itself brought in untrusted
	// content (a web search, a tool result marked untrusted), not when it
	// only inherited that from earlier ones.
	untrusted bool
	at        time.Time
}

// shortTerm is the pipeline's conversation memory. Guarded by Pipeline.mu.
type shortTerm struct {
	turns int           // exchanges kept; 0 means the memory is off
	ttl   time.Duration // idle time after which it is forgotten; <= 0 never
	ex    []exchange    // oldest first
	gen   uint64        // changes whenever the memory is cleared
}

// SetContext sets how many exchanges the AI remembers across commands and
// how long after the last one it forgets them (config ai.context.turns and
// ai.context.ttl). Zero turns turns it off; a ttl of zero uses
// DefaultContextTTL and a negative one never expires. It applies at once.
func (p *Pipeline) SetContext(turns int, ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setContextLocked(turns, ttl)
}

func (p *Pipeline) setContextLocked(turns int, ttl time.Duration) {
	if ttl == 0 {
		ttl = DefaultContextTTL
	}
	p.mem.turns, p.mem.ttl = max(turns, 0), ttl
	if p.mem.turns == 0 {
		p.forgetLocked()
	} else if n := len(p.mem.ex) - p.mem.turns; n > 0 {
		p.mem.ex = slices.Clone(p.mem.ex[n:])
	}
}

// ForgetContext clears the conversation memory ("start over").
func (p *Pipeline) ForgetContext() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetLocked()
}

// forgetLocked clears the memory and the conversation a question left open.
// p.mu must be held.
func (p *Pipeline) forgetLocked() {
	p.mem.ex = nil
	p.mem.gen++
	p.fu.conv = nil
}

// recall returns the remembered conversation to send before a new request,
// whether any of it is untrusted, and a token for remember. on is false when
// the memory is off. An expired memory is forgotten here.
func (p *Pipeline) recall() (hist []llm.Message, untrusted bool, gen uint64, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := &p.mem
	if m.turns <= 0 {
		return nil, false, m.gen, false
	}
	if n := len(m.ex); n > 0 && m.ttl > 0 && p.now().Sub(m.ex[n-1].at) > m.ttl {
		p.forgetLocked()
	}
	for _, e := range m.ex {
		hist = append(hist, e.msgs...)
		untrusted = untrusted || e.untrusted
	}
	return hist, untrusted, m.gen, true
}

// remember adds a finished exchange (gen is what recall returned: nothing is
// added if the memory was cleared meanwhile). The exchange before it is
// compacted: its tool calls and raw search results go, and a note of the
// pages it used stays.
func (p *Pipeline) remember(gen uint64, msgs []llm.Message, untrusted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := &p.mem
	if m.turns <= 0 || m.gen != gen || len(msgs) == 0 {
		return
	}
	if n := len(m.ex); n > 0 {
		if c := compactExchange(m.ex[n-1].msgs); c != nil {
			m.ex[n-1].msgs = c
		} else {
			m.ex = m.ex[:n-1]
		}
	}
	m.ex = append(m.ex, exchange{msgs: slices.Clone(msgs), untrusted: untrusted, at: p.now()})
	if n := len(m.ex) - m.turns; n > 0 {
		m.ex = slices.Clone(m.ex[n:])
	}
}

// compactExchange reduces an exchange to what an old one needs: the request,
// the words of the reply, and a note of the pages it drew on, so "open the
// first two links" still works. Tool calls, tool results and the provider's
// raw search blocks (which are large) are dropped. It returns nil when
// nothing of the reply is left.
func compactExchange(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 || msgs[0].Role != llm.User {
		return nil
	}
	plain := len(msgs) == 2 && msgs[1].Role == llm.Assistant && len(msgs[1].Raw) == 0 && len(msgs[1].ToolCalls) == 0
	if plain && len(msgs[1].Sources) == 0 {
		return msgs
	}
	var texts []string
	var srcs []llm.Source
	seen := map[string]bool{}
	for _, m := range msgs[1:] {
		if m.Role != llm.Assistant {
			continue
		}
		if t := strings.TrimSpace(m.Text); t != "" {
			texts = append(texts, t)
		}
		for _, s := range m.Sources {
			if !seen[s.URL] && len(srcs) < maxNoteSources {
				seen[s.URL] = true
				srcs = append(srcs, s)
			}
		}
	}
	text := strings.Join(texts, " ") + sourcesNote(srcs)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []llm.Message{msgs[0], {Role: llm.Assistant, Text: text}}
}

// sourcesNote is the text an old answer keeps in place of its search
// results, or "" when it used no pages.
func sourcesNote(srcs []llm.Source) string {
	if len(srcs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n[Sources from that answer:")
	for i, s := range srcs {
		title := strings.Join(strings.Fields(s.Title), " ")
		if r := []rune(title); len(r) > maxNoteTitle {
			title = string(r[:maxNoteTitle]) + "..."
		}
		sep := ""
		if i > 0 {
			sep = ";"
		}
		if title == "" {
			fmt.Fprintf(&b, "%s %d. %s", sep, i+1, s.URL)
		} else {
			fmt.Fprintf(&b, "%s %d. %s (%s)", sep, i+1, title, s.URL)
		}
	}
	b.WriteString("]")
	return b.String()
}

// ContextRules is the part of the system prompt about the conversation the
// AI carries across commands. It is empty when the memory is off. Nothing in
// it changes between requests, so providers can cache it.
func ContextRules(on bool) string {
	if !on {
		return ""
	}
	return `The conversation may include your earlier answers from the last few minutes, said in separate commands: use them when the user says "that", "those", "the first one" or "open the links". A line like "[Sources from that answer: 1. Title (address); ...]" lists the pages that answer used. When the user asks to open the links, pages or sources from an earlier answer, call open_url once with urls set to the ones they mean (at most five), so they confirm once; never read addresses aloud, and never write a "Sources" line yourself.`
}

// ForgetPhrases are the commands, said as a whole, that clear the
// conversation memory.
var ForgetPhrases = []string{
	"forget that", "forget this", "start over", "start again", "start fresh",
	"new topic", "new conversation", "forget this conversation", "forget the conversation",
	"clear the conversation", "clear the context", "forget what we were talking about",
}

// ForgetRule matches "forget that", "start over" and "new topic": it clears
// the short-term conversation memory. It only changes Footman's own state, so
// it is a Read action.
func ForgetRule() FastRule {
	return FastRule{Name: "forget", Permission: plugins.Read, Match: func(text string) (FastAction, bool) {
		w := strings.Join(dropWords(dictation.NormWords(text), "please"), " ")
		if !slices.Contains(ForgetPhrases, w) {
			return nil, false
		}
		return func(_ context.Context, p *Pipeline) (string, error) {
			p.ForgetContext()
			return "Okay, starting fresh.", nil
		}, true
	}}
}
