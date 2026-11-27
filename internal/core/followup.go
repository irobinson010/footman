package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/speech"
)

// Follow-up listening and voice confirmation (ADR-0047).
//
// When a spoken reply ends with a question, or Footman asks the owner to
// confirm an action, the pipeline keeps listening for the answer with no wake
// word: a follow-up window. The microphone is already open (wake listening),
// so the window is a rule about which speech counts: speech that began after
// the guard that follows the end of Footman's own voice, and before the
// window's timeout, is handled as TriggerFollowUp. The rule is stated in
// stream offsets (the frames' own clock), not wall time, so the audio of the
// reply, and its echo, can never be mistaken for the answer.

// Follow-up window defaults.
const (
	// DefaultFollowUpTimeout is how long a window waits in silence
	// (config speech.followup.timeout).
	DefaultFollowUpTimeout = 8 * time.Second
	// DefaultFollowUpGuard is kept between the end of Footman's speech and
	// the start of the window, so the room's echo of it is not heard.
	DefaultFollowUpGuard = 250 * time.Millisecond
)

// followUp is the follow-up window and what goes with it. Guarded by
// Pipeline.mu, except the capture clock.
type followUp struct {
	enabled bool
	timeout time.Duration

	// open: a window accepts speech. late: it timed out, but speech that
	// began before its deadline and is still being handled counts.
	open, late bool
	// confirm: the window answers the pending confirmation, so a claimed
	// segment is a yes or no and not a command.
	confirm bool
	// gen, fence and deadline say which speech counts: from the capture
	// session gen, starting at or after fence and at or before deadline
	// (stream offsets).
	gen             uint64
	fence, deadline time.Duration
	seq             uint64
	stop            chan struct{} // ends the window's timer

	// conv is the AI conversation a question left open, for the answer.
	conv *convo

	// pending is the confirmation waiting for a voice answer, and answer
	// resolves it (SetConfirmAnswerer).
	pending string
	answer  func(id string, allow bool) error

	// The capture clock: the session and the offset of the latest frame.
	capGen atomic.Uint64
	capPos atomic.Int64
}

// convo is an AI conversation to carry into the follow-up window.
type convo struct {
	msgs      []llm.Message
	untrusted bool
}

type followUpKey struct{}

// SetFollowUp turns follow-up listening on or off and sets how long a window
// waits in silence (config speech.followup.enabled and .timeout). It applies
// to the next window.
func (p *Pipeline) SetFollowUp(enabled bool, timeout time.Duration) {
	p.mu.Lock()
	p.fu.enabled, p.fu.timeout = enabled, timeout
	if !enabled && p.fu.open && !p.fu.confirm {
		p.closeFollowLocked()
	}
	p.mu.Unlock()
}

// SetConfirmAnswerer gives the pipeline the way to answer a pending
// confirmation by voice (the daemon passes permissions.Broker.Answer with
// the voice path). Without one, spoken yes and no are not listened for.
func (p *Pipeline) SetConfirmAnswerer(fn func(id string, allow bool) error) {
	p.mu.Lock()
	p.fu.answer = fn
	p.mu.Unlock()
}

func (p *Pipeline) after(d time.Duration) <-chan time.Time {
	if p.cfg.After != nil {
		return p.cfg.After(d)
	}
	return time.After(d)
}

// trackCapture records the stream offset of the latest microphone frame, and
// starts a new capture session, so a window's fence is read against the
// right clock.
func (p *Pipeline) trackCapture(ctx context.Context, in <-chan audio.Frame) <-chan audio.Frame {
	p.fu.capGen.Add(1)
	p.fu.capPos.Store(0)
	out := make(chan audio.Frame)
	go func() {
		defer close(out)
		for f := range in {
			p.fu.capPos.Store(int64(f.Offset + f.Duration()))
			select {
			case out <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// followReadyLocked reports whether a window could hear anything: the wake
// microphone is open, and no dictation session is running (it takes every
// segment, ADR-0045, including its own file-choice questions).
func (p *Pipeline) followReadyLocked() bool {
	return p.fu.enabled && p.fu.timeout > 0 && p.d.Wake != nil && p.micCancel != nil &&
		!p.muted && !p.wakeOff && p.rec == nil && p.sess == nil
}

// openFollowUp opens the window after Footman's speech has ended, and plays
// the listening cue if there is one. confirm says it answers a pending
// confirmation. It does nothing when follow-up is off or the microphone is
// not listening.
func (p *Pipeline) openFollowUp(ctx context.Context, confirm bool) {
	p.mu.Lock()
	ready := p.followReadyLocked()
	p.mu.Unlock()
	if !ready || ctx.Err() != nil {
		return
	}
	// The cue is a sound too: the guard is measured after it. It is the
	// optional listening cue (Config.Earcon); the clip bank has none.
	if len(p.cfg.Earcon.Samples) > 0 && !p.Quiet() {
		_ = p.d.Platform.Audio().Play(ctx, p.cfg.Speaker, audio.NewClipStream(p.cfg.Earcon, 320))
	}
	guard := p.cfg.FollowUpGuard
	if guard <= 0 {
		guard = DefaultFollowUpGuard
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.followReadyLocked() || ctx.Err() != nil {
		return
	}
	p.closeFollowLocked()
	f := &p.fu
	f.seq++
	f.open, f.late, f.confirm = true, false, confirm
	f.gen = f.capGen.Load()
	f.fence = time.Duration(f.capPos.Load()) + guard
	f.deadline = f.fence + f.timeout
	f.stop = make(chan struct{})
	timer := p.after(guard + f.timeout)
	go func(seq uint64, stop chan struct{}) {
		select {
		case <-timer:
			p.expireFollowUp(seq)
		case <-stop:
		case <-p.ctx.Done():
		}
	}(f.seq, f.stop)
	p.setStateLocked(FollowUp, p.busy)
}

// expireFollowUp closes window seq after its silence. A reply window keeps
// counting speech that began before its deadline and is still being
// transcribed.
func (p *Pipeline) expireFollowUp(seq uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := &p.fu
	if f.seq != seq || !f.open {
		return
	}
	late := !f.confirm
	p.closeFollowLocked()
	f.late = late
}

// closeFollowLocked closes the window (not the conversation): the state goes
// back to what it was. p.mu must be held.
func (p *Pipeline) closeFollowLocked() {
	f := &p.fu
	if f.stop != nil {
		close(f.stop)
		f.stop = nil
	}
	f.open, f.late, f.confirm = false, false, false
	if p.state == FollowUp {
		switch {
		case p.armed != 0:
			p.setStateLocked(Listening, 0)
		case p.busy != 0:
			p.setStateLocked(RunningTool, p.busy)
		default:
			p.setStateLocked(Idle, 0)
		}
	}
}

// closeFollowUp closes the window and forgets the conversation: the
// microphone stopped, or the owner said they were done.
func (p *Pipeline) closeFollowUp() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeFollowLocked()
	p.fu.conv = nil
}

// consumeFollowUp ends a reply window when its answer is acted on. A
// confirmation window stays until the confirmation ends.
func (p *Pipeline) consumeFollowUp() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.fu.confirm {
		p.closeFollowLocked()
	}
}

// claimFollowUpLocked hands seg to an open window, if it began inside it.
// p.mu must be held.
func (p *Pipeline) claimFollowUpLocked(seg speech.Segment) (claim, bool) {
	f := &p.fu
	if !f.open && !f.late {
		return claim{}, false
	}
	if f.gen != f.capGen.Load() || seg.Start < f.fence || seg.Start > f.deadline {
		return claim{}, false
	}
	if n := audio.Speech.Samples(p.cfg.MaxCommand); len(seg.Audio.Samples) > n {
		seg.Audio.Samples = seg.Audio.Samples[:n]
	}
	return claim{trig: TriggerFollowUp, seg: seg, answer: f.confirm}, true
}

// handleBusySegment handles speech claimed while another utterance is being
// handled (ADR-0017): only the break-glass stop gets through, and a yes or
// no to a pending confirmation, which is why the utterance is busy.
func (p *Pipeline) handleBusySegment(ctx context.Context, c claim) {
	id, pending := p.pendingConfirmation()
	if !(c.trig == TriggerWake || (c.trig == TriggerFollowUp && pending)) {
		return
	}
	t, err := p.d.STT.Transcribe(ctx, c.seg.Audio)
	text, ok := t.Text, err == nil
	if ok && c.verify {
		text, ok = p.cfg.Router.MatchWake(text)
	}
	if !ok {
		return
	}
	if p.cfg.Router.BreakGlass(c.trig, text) {
		p.stop(StopVoice)
		return
	}
	if pending {
		p.voiceAnswer(id, c.trig, text)
	}
}

func (p *Pipeline) pendingConfirmation() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fu.pending, p.fu.pending != "" && p.fu.answer != nil
}

// voiceAnswer answers confirmation id with what was said, if it is an
// answer. The whole utterance has to be one (ParseAnswer).
func (p *Pipeline) voiceAnswer(id string, trig Trigger, text string) {
	r := p.cfg.Router.Route(trig, text)
	if r.Kind != RouteCommand {
		return
	}
	a := ParseAnswer(r.Text)
	if a == AnswerNone && closesFollowUp(r.Text) {
		a = AnswerNo
	}
	if a == AnswerNone {
		return
	}
	p.mu.Lock()
	answer := p.fu.answer
	p.mu.Unlock()
	if answer != nil {
		// A refusal (the request is click-only, or already answered) is
		// not an error here: the owner can still use the notification.
		_ = answer(id, a == AnswerYes)
	}
}

// ConfirmPrompt is what a confirmer says and listens for while a
// confirmation waits (PromptConfirm).
type ConfirmPrompt struct {
	// ID identifies the confirmation to the answerer.
	ID string
	// Question is spoken, such as "Shall I open example.com?". Empty means
	// the persona's confirm line.
	Question string
	// ClickOnly means voice may not answer: it is told to confirm on screen,
	// and nothing is listened for.
	ClickOnly bool
	// Level is the action's permission level, for the click-only line.
	Level string
}

// PromptConfirm is called by a confirmer, on the goroutine of the command
// that made the call, when it starts waiting for the owner. If the reply is
// being spoken it says the question, and when the question can be answered by
// voice it opens a follow-up window for the answer: the pipeline then
// answers the confirmation when the owner says yes or no (SetConfirmAnswerer).
// A click-only confirmation is announced and not listened for. It returns
// when the question has been spoken, and the function it returns must be
// called when the confirmation ends, to close the window.
func PromptConfirm(ctx context.Context, c ConfirmPrompt) (end func()) {
	noop := func() {}
	sp := speakerFrom(ctx)
	if sp == nil {
		return noop
	}
	return sp.p.promptConfirm(ctx, sp, c)
}

func (p *Pipeline) promptConfirm(ctx context.Context, sp *speaker, c ConfirmPrompt) func() {
	noop := func() {}
	text := c.Question
	switch {
	case c.ClickOnly:
		level := c.Level
		if level == "" {
			level = "sensitive"
		}
		text = fmt.Sprintf("This is a %s action. Please confirm on screen.", level)
	case text == "":
		text = p.line(LineConfirm, "")
	}
	if text == "" {
		return noop
	}
	sp.Break()
	sp.Feed(text)
	if err := sp.Sync(); err != nil || ctx.Err() != nil || c.ClickOnly {
		return noop
	}
	p.mu.Lock()
	if p.fu.answer == nil {
		p.mu.Unlock()
		return noop
	}
	// A spoken "yes" is also heard after the window times out, when it is
	// said after the wake word.
	p.fu.pending = c.ID
	p.mu.Unlock()
	p.openFollowUp(ctx, true)
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.fu.pending == c.ID {
			p.fu.pending = ""
			if p.fu.confirm {
				p.closeFollowLocked()
			}
		}
	}
}

// ---- the conversation a question leaves open ----

func (p *Pipeline) stashConvo(msgs []llm.Message, untrusted bool) {
	p.mu.Lock()
	p.fu.conv = &convo{msgs: slices.Clone(msgs), untrusted: untrusted}
	p.mu.Unlock()
}

func (p *Pipeline) takeConvo() *convo {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.fu.conv
	p.fu.conv = nil
	return c
}

func (p *Pipeline) dropConvo() {
	p.mu.Lock()
	p.fu.conv = nil
	p.mu.Unlock()
}

// ---- what is said ----

// Answer is what an utterance says to a yes or no question.
type Answer int

// Answers.
const (
	AnswerNone Answer = iota
	AnswerYes
	AnswerNo
)

// Phrases that answer a confirmation (ADR-0047). The whole utterance has to
// be one of them, apart from case and punctuation, so "yes, delete
// everything" is not a yes. Everything is lower case with straight
// apostrophes.
var (
	YesPhrases = []string{"yes", "yes please", "do it", "go ahead", "confirm"}
	NoPhrases  = []string{"no", "cancel", "don't"}
	// DonePhrases end a follow-up window. They deny a confirmation.
	DonePhrases = []string{"never mind", "nevermind", "that's all", "cancel"}
)

func phraseWords(text string) string {
	text = strings.NewReplacer("’", "'", "‘", "'").Replace(text)
	return strings.Join(dictation.NormWords(text), " ")
}

func inPhrases(text string, phrases []string) bool {
	w := phraseWords(text)
	return w != "" && slices.Contains(phrases, w)
}

// ParseAnswer says whether text, as a whole, is a yes or a no.
func ParseAnswer(text string) Answer {
	switch {
	case inPhrases(text, YesPhrases):
		return AnswerYes
	case inPhrases(text, NoPhrases):
		return AnswerNo
	}
	return AnswerNone
}

// closesFollowUp reports whether text, as a whole, says the owner is done.
func closesFollowUp(text string) bool { return inPhrases(text, DonePhrases) }

// endsWithQuestion reports whether the last sentence of a reply is a
// question: it ends in "?" (closing quotes, brackets and emphasis marks
// aside).
func endsWithQuestion(text string) bool {
	s := strings.TrimRight(text, " \t\r\n\"'”’)]}*_")
	return strings.HasSuffix(s, "?")
}
