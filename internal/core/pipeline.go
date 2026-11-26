package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/plugins"
	"github.com/irobinson010/footman/internal/speech"
)

// ErrBusy is returned by Submit while another utterance is being handled.
var ErrBusy = errors.New("core: busy with another utterance")

// Deps are the pipeline's collaborators. Bus, Platform and STT are required;
// the rest are optional, as noted.
type Deps struct {
	// Bus receives every event.
	Bus *bus.Bus
	// Platform provides input, launching, notifications, the tray and audio.
	Platform platform.Platform
	// VAD splits microphone audio into utterances. Run needs it.
	VAD speech.VAD
	// Wake spots the wake word. Nil means hotkeys only.
	Wake speech.WakeWord
	// STT transcribes utterances.
	STT speech.STT
	// TTS speaks replies. Nil means replies are shown as notifications.
	TTS speech.TTS
	// LLM answers commands the fast path does not match. Nil means only the
	// fast path works.
	LLM llm.Provider
	// Tools are offered to the LLM.
	Tools []plugins.Tool
	// Worker runs long tasks in the background. Nil means the AI is not
	// offered the start_task tool.
	Worker jobs.Worker
	// Layout decides what auto insertion can type. Nil means the platform
	// Input's own check when it has one (on Linux, the compositor's keymap),
	// else dictation.ASCIILayout.
	Layout dictation.Layout
	// Gate decides whether a tool call may run (default DefaultGate).
	Gate Gate
}

// Config is the pipeline's behaviour. The zero value is usable.
type Config struct {
	// WakeWords are stripped from the start of transcripts that kept them
	// (default "footman").
	WakeWords []string
	// DictateHotkey, CommandHotkey and StopHotkey are the global hotkeys Run
	// registers; empty ones are skipped.
	DictateHotkey, CommandHotkey, StopHotkey string
	// Mic and Speaker are device IDs; empty means the default.
	Mic, Speaker string
	// AGC levels the captured microphone audio before the VAD and wake word
	// (ADR-0035); AGCMaxGainDB caps its gain (0 means the default).
	AGC          bool
	AGCMaxGainDB float64
	// DictateMode and CommandMode say how the dictation and command hotkeys
	// work (default HoldToTalk).
	DictateMode, CommandMode TalkMode
	// ReleaseTail is how long a hold-to-talk recording goes on after the key
	// is released (default DefaultReleaseTail).
	ReleaseTail time.Duration
	// MaxUtterance caps one push-to-talk recording (default
	// DefaultMaxUtterance).
	MaxUtterance time.Duration
	// MinCommand is the least speech that must follow a wake word for the
	// utterance to count; shorter is a false wake and is passed over (zero
	// means any; the daemon uses DefaultMinCommand).
	MinCommand time.Duration
	// MaxCommand caps the audio of one command said after the wake word
	// (default DefaultMaxCommand).
	MaxCommand time.Duration
	// WakeWindow is how long after the wake word a command may start before
	// the wake is dropped (default DefaultWakeWindow).
	WakeWindow time.Duration
	// VerifyWake turns on the second stage of the wake word (ADR-0041): the
	// audio is transcribed from just before the wake word, and the turn is
	// dropped unless the transcript starts with a wake word (Router.MatchWake).
	// The daemon sets it from speech.wake.verify.
	VerifyWake bool
	// Quiet is quiet mode: no earcon or acknowledgement sound, and replies
	// are shown rather than spoken. See SetQuiet.
	Quiet bool
	// Earcon is a short sound played when the wake word is heard, unless
	// quiet. Empty means none. It should be brief and soft: the microphone
	// stays open while it plays.
	Earcon audio.Clip
	// Insert is the dictation insert mode (default auto).
	Insert dictation.InsertMode
	// TypeMax is the longest text auto mode types (default
	// dictation.DefaultTypeMax).
	TypeMax int
	// Ack supplies the persona's acknowledgement clip, played as a command
	// starts (the clip bank, or FixedAck). Nil means none.
	Ack AckSource
	// Voice is the TTS voice for replies, until SetPersona.
	Voice speech.Voice
	// Speak is the reply policy (config replies.speak): "always",
	// "when-voice" (the default: only for requests that came by voice) or
	// "never". Change it while running with SetReplyPolicy.
	Speak string
	// Notify shows every reply as a notification too, even one that is
	// spoken (config replies.notify). A reply that is not spoken is always
	// shown. Change it while running with SetReplyNotify.
	Notify bool
	// QuietHours is a range like "22:00-07:00" with no spoken replies (config
	// replies.quiet_hours). Empty means none.
	QuietHours string
	// Now is the clock for quiet hours (default time.Now).
	Now func() time.Time
	// System is the LLM system prompt, until SetPersona.
	System string
	// Model overrides the provider's default model.
	Model string
	// MaxToolRounds caps LLM round trips per command (default 8).
	MaxToolRounds int
	// NoCommands turns spoken formatting commands off (config
	// dictation.commands = off).
	NoCommands bool
	// NoTerminal turns off switching to code mode when the focused app is a
	// terminal (config dictation.terminal_mode).
	NoTerminal bool
	// Language picks the phrase lists (default "en").
	Language string
	// Cleaner tidies dictation (default dictation.NewCleaner(nil, nil)).
	Cleaner *dictation.Cleaner
	// Router routes utterances (default NewRouter(WakeWords, nil, nil)).
	Router *Router
	// FastRules are tried before the LLM (default DefaultFastRules(nil)).
	FastRules []FastRule
}

// Pipeline is the voice pipeline state machine. Create one with New, drive it
// with Run (microphone and hotkeys) or Submit (text), and stop everything with
// Stop. A Pipeline is safe for concurrent use.
type Pipeline struct {
	d         Deps
	cfg       Config
	formatter *dictation.Formatter

	// ctx bounds background jobs; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // job watchers

	mu         sync.Mutex
	state      State
	turnSeq    uint64
	busy       uint64 // the turn being handled, or 0
	turns      map[uint64]context.CancelFunc
	armed      Trigger
	armedAt    time.Duration // stream offset the wake word ended at, if armedTimed
	armedTimed bool
	// armedHead is the audio of a wake word said on its own, kept for the
	// second stage to check together with the command that follows
	// (VerifyWake).
	armedHead speech.Audio
	// recent is the last segment nothing claimed, cut to its last
	// wakeLookback, in case the spotter reports a wake word after the VAD
	// has ended its segment (VerifyWake). It is never transcribed unless a
	// wake word follows.
	recent     speech.Segment
	muted      bool
	wakeOff    bool          // wake listening switched off (SetWakeListening)
	quiet      bool          // quiet mode
	changed    chan struct{} // closed, and replaced, when muted, wakeOff or rec change
	micCancel  context.CancelFunc
	mic        micLoop
	rec        *recording // the push-to-talk recording, if any
	lastIns    Insertion
	codeMode   *bool // spoken code mode on/off; nil follows the focused app
	scratchLen int   // characters of the last insertion "scratch that" can take back
	lastInsAt  time.Time
	hotkeyErrs map[string]string // hotkeys that could not be registered
	jobSeq     int
	jobs       map[string]jobs.Job
	persona    Persona // voice, system prompt and lines (SetPersona)
}

// New builds a pipeline in the Idle state.
func New(d Deps, cfg Config) (*Pipeline, error) {
	if d.Bus == nil || d.Platform == nil || d.STT == nil {
		return nil, errors.New("core: Bus, Platform and STT are required")
	}
	if d.Layout == nil {
		if l, ok := d.Platform.Input().(dictation.Layout); ok {
			d.Layout = l
		} else {
			d.Layout = dictation.ASCIILayout{}
		}
	}
	if d.Gate == nil {
		d.Gate = DefaultGate{}
	}
	if cfg.WakeWords == nil {
		cfg.WakeWords = []string{"footman"}
	}
	if cfg.Insert == "" {
		cfg.Insert = dictation.InsertAuto
	}
	if cfg.DictateMode == "" {
		cfg.DictateMode = HoldToTalk
	}
	if cfg.CommandMode == "" {
		cfg.CommandMode = HoldToTalk
	}
	if cfg.ReleaseTail <= 0 {
		cfg.ReleaseTail = DefaultReleaseTail
	}
	if cfg.MaxUtterance <= 0 {
		cfg.MaxUtterance = DefaultMaxUtterance
	}
	if cfg.TypeMax <= 0 {
		cfg.TypeMax = dictation.DefaultTypeMax
	}
	if cfg.MaxCommand <= 0 {
		cfg.MaxCommand = DefaultMaxCommand
	}
	if cfg.WakeWindow <= 0 {
		cfg.WakeWindow = DefaultWakeWindow
	}
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = 8
	}
	if cfg.Cleaner == nil {
		// Spoken commands are the formatter's job (ADR-0032).
		cfg.Cleaner = dictation.NewCleaner(nil, []dictation.DictationCommand{})
	}
	if cfg.Router == nil {
		cfg.Router = NewRouter(cfg.WakeWords, nil, nil)
	}
	if cfg.FastRules == nil {
		cfg.FastRules = DefaultFastRules(nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lang := cfg.Language
	if lang == "" {
		lang = "en"
	}
	return &Pipeline{
		d: d, cfg: cfg, formatter: dictation.NewFormatter(lang), ctx: ctx, cancel: cancel,
		state: Idle, quiet: cfg.Quiet, changed: make(chan struct{}),
		turns:   map[uint64]context.CancelFunc{},
		jobs:    map[string]jobs.Job{},
		persona: Persona{Voice: cfg.Voice, System: cfg.System},
	}, nil
}

// Close cancels every background job and waits for them to wind down. The
// pipeline must not be used afterwards.
func (p *Pipeline) Close() {
	p.cancel()
	p.wg.Wait()
}

// State returns the current state.
func (p *Pipeline) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// LastInsertion returns the most recent dictated insertion and when it
// finished; ok is false if nothing has been dictated yet.
func (p *Pipeline) LastInsertion() (ins Insertion, at time.Time, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastIns, p.lastInsAt, !p.lastInsAt.IsZero()
}

// Jobs returns the IDs of the background jobs that have not finished yet.
func (p *Pipeline) Jobs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Sorted(maps.Keys(p.jobs))
}

// setStateLocked changes state and publishes it. p.mu must be held.
func (p *Pipeline) setStateLocked(to State, turn uint64) {
	if p.state == to {
		return
	}
	from := p.state
	p.state = to
	bus.Publish(p.d.Bus, StateChanged, StateChange{From: from, To: to, Turn: turn})
}

// setState changes state for turn, unless the turn has been cancelled by a
// stop (whose own state changes win).
func (p *Pipeline) setState(turn uint64, to State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy == turn {
		p.setStateLocked(to, turn)
	}
}

// Arm starts listening for an utterance as if trig had fired: the next
// end of speech is handled as a wake-word, dictation or command utterance.
// Run calls it for the wake word and hotkeys; the tray and API may too.
func (p *Pipeline) Arm(trig Trigger) {
	p.arm(trig, 0, false)
}

// arm is Arm; for a wake word, at is the stream offset where it ended, so
// the command is cut from just before that point (timed is true).
func (p *Pipeline) arm(trig Trigger, at time.Duration, timed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed, p.armedAt, p.armedTimed = trig, at, timed
	p.armedHead = speech.Audio{}
	if timed && p.cfg.VerifyWake && p.recent.End > at-wakeLookback {
		p.armedHead = trimFrom(p.recent, at-wakeLookback).Audio
	}
	p.recent = speech.Segment{}
	if p.busy == 0 {
		p.setStateLocked(Listening, 0)
	}
}

// disarm drops an armed wake that nothing followed, silently (design §17).
func (p *Pipeline) disarm() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.armed == 0 {
		return
	}
	p.clearArmLocked()
	if p.busy == 0 && p.rec == nil {
		p.setStateLocked(Idle, 0)
	}
}

// clearArmLocked forgets the armed trigger. p.mu must be held.
func (p *Pipeline) clearArmLocked() {
	p.armed, p.armedTimed = 0, false
	p.armedHead = speech.Audio{}
}

// claim is a segment handed to the armed trigger.
type claim struct {
	trig Trigger
	seg  speech.Segment
	// verify means seg starts before the wake word and the transcript must
	// begin with one (ADR-0041).
	verify bool
}

// claimSegment hands seg to the armed trigger. For a wake word it first cuts
// the audio to the command: from a pre-roll before the end of the wake word
// (so the first word after it is not clipped, nor the wake word kept), to at
// most MaxCommand. A segment that ended before the wake word, or has less
// than MinCommand left after it (the wake word itself, said alone), is not
// claimed and the wake stays armed for the next one. ok is false then, or
// when nothing is armed.
//
// With VerifyWake the cut starts earlier, up to wakeLookback before the end
// of the wake word (at the start of the segment if that is later), so the
// second stage can hear the wake word itself. A wake word said alone is kept
// and put in front of the next segment, with a short gap.
func (p *Pipeline) claimSegment(seg speech.Segment) (c claim, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.armed == 0 {
		if p.cfg.VerifyWake {
			p.recent = trimFrom(seg, seg.End-wakeLookback)
		}
		return claim{}, false
	}
	if p.armedTimed {
		verify := p.cfg.VerifyWake
		if seg.End <= p.armedAt {
			if verify && seg.End > p.armedAt-wakeLookback {
				// The spotter reports late, so a wake word said alone can
				// be a segment that ended before it did.
				p.armedHead = trimFrom(seg, p.armedAt-wakeLookback).Audio
			}
			return claim{}, false
		}
		if seg.Start-p.armedAt > p.cfg.WakeWindow {
			// Expired in stream time; the timer in listen does the same in
			// wall-clock time, which runs faster than a fed-in file.
			p.clearArmLocked()
			if p.busy == 0 && p.rec == nil {
				p.setStateLocked(Idle, 0)
			}
			return claim{}, false
		}
		cut := p.armedAt - wakePreRoll
		if seg.End-max(cut, seg.Start) < max(p.cfg.MinCommand, time.Millisecond) {
			if verify && seg.Start <= p.armedAt {
				p.armedHead = trimFrom(seg, p.armedAt-wakeLookback).Audio
			}
			return claim{}, false
		}
		limit := p.cfg.MaxCommand
		if verify {
			cut = p.armedAt - wakeLookback
			limit += wakeLookback
		}
		// A segment that started well before the wake word was reported
		// holds it; one that did not needs the wake word kept from before.
		needHead := seg.Start >= p.armedAt-wakePreRoll
		seg = trimFrom(seg, cut)
		if n := audio.Speech.Samples(limit); len(seg.Audio.Samples) > n {
			seg.Audio.Samples = seg.Audio.Samples[:n]
		}
		if verify && needHead && len(p.armedHead.Samples) > 0 {
			joined := make([]float32, 0, len(p.armedHead.Samples)+audio.Speech.Samples(wakeGap)+len(seg.Audio.Samples))
			joined = append(joined, p.armedHead.Samples...)
			joined = append(joined, make([]float32, audio.Speech.Samples(wakeGap))...)
			seg.Audio.Samples = append(joined, seg.Audio.Samples...)
		}
		c = claim{trig: p.armed, seg: seg, verify: verify}
	} else {
		c = claim{trig: p.armed, seg: seg}
	}
	p.clearArmLocked()
	return c, true
}

// trimFrom drops the audio of seg before the stream offset from.
func trimFrom(seg speech.Segment, from time.Duration) speech.Segment {
	if n := audio.Speech.Samples(from - seg.Start); n > 0 {
		seg.Audio.Samples = seg.Audio.Samples[min(n, len(seg.Audio.Samples)):]
		seg.Start = from
	}
	return seg
}

// beginTurn claims the pipeline for a new utterance. It returns 0 if another
// utterance is being handled.
func (p *Pipeline) beginTurn(ctx context.Context) (uint64, context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy != 0 {
		return 0, nil
	}
	p.turnSeq++
	id := p.turnSeq
	tctx, cancel := context.WithCancel(ctx)
	p.busy = id
	p.turns[id] = cancel
	return id, tctx
}

// endTurn releases the pipeline and returns to Idle, or to Listening if a
// trigger fired meanwhile.
func (p *Pipeline) endTurn(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel, ok := p.turns[id]; ok {
		cancel()
		delete(p.turns, id)
	}
	if p.busy != id {
		return
	}
	p.busy = 0
	if p.armed != 0 {
		p.setStateLocked(Listening, 0)
	} else {
		p.setStateLocked(Idle, 0)
	}
}

func (p *Pipeline) fail(turn uint64, err error) error {
	p.mu.Lock()
	st := p.state
	p.mu.Unlock()
	bus.Publish(p.d.Bus, Failed, Failure{Turn: turn, State: st, Err: err.Error()})
	return err
}

// Utterance is text to handle, as if it had been spoken after trig.
type Utterance struct {
	Trigger Trigger
	Text    string
	// Typed is true when the text did not come by voice (the local API's
	// say.text); the reply policy "when-voice" does not speak the reply.
	Typed bool
	// Ended is when the speech ended (the push-to-talk key came up), for
	// the latency reported with an insertion; zero if unknown.
	Ended time.Time
}

// Submit handles one utterance whose text is already known, such as text sent
// through the local API, and returns when the pipeline is idle again
// (long tasks keep running as jobs). A break-glass stop phrase after the wake
// word is honoured even while another utterance is being handled; anything
// else then fails with ErrBusy.
func (p *Pipeline) Submit(ctx context.Context, u Utterance) error {
	if p.cfg.Router.BreakGlass(u.Trigger, u.Text) {
		p.stop(StopVoice)
		return nil
	}
	id, tctx := p.beginTurn(ctx)
	if id == 0 {
		return ErrBusy
	}
	defer p.endTurn(id)
	return p.handle(tctx, id, u)
}

// handleSegment handles one utterance of captured speech.
// ended is when the VAD handed over the segment, the first moment the end of
// speech is known.
func (p *Pipeline) handleSegment(ctx context.Context, c claim, ended time.Time) {
	id, tctx := p.beginTurn(ctx)
	if id == 0 {
		// Busy: only the break-glass stop can get through (ADR-0017).
		if c.trig == TriggerWake {
			t, err := p.d.STT.Transcribe(ctx, c.seg.Audio)
			text, ok := t.Text, err == nil
			if ok && c.verify {
				text, ok = p.cfg.Router.MatchWake(text)
			}
			if ok && p.cfg.Router.BreakGlass(c.trig, text) {
				p.stop(StopVoice)
			}
		}
		return
	}
	defer p.endTurn(id)
	p.transcribeAndHandle(tctx, id, c, ended)
}

// transcribeAndHandle transcribes the claimed audio for turn id and handles
// the text. ended is when the speech ended, if known. With c.verify, a
// transcript that does not start with a wake word is dropped (ADR-0041): it
// is reported on WakeRejected, without its text, and not on Transcribed.
func (p *Pipeline) transcribeAndHandle(ctx context.Context, id uint64, c claim, ended time.Time) {
	p.setState(id, Transcribing)
	start := time.Now()
	t, err := p.d.STT.Transcribe(ctx, c.seg.Audio)
	if err != nil {
		_ = p.fail(id, fmt.Errorf("transcribe: %w", err))
		return
	}
	took := time.Since(start)
	text := t.Text
	if c.verify {
		rest, ok := p.cfg.Router.MatchWake(text)
		if !ok {
			reason := WakeNoMatch
			if len(dictation.NormWords(text)) == 0 {
				reason = WakeNoSpeech
			}
			bus.Publish(p.d.Bus, WakeRejected, WakeRejection{Turn: id, Reason: reason, Audio: c.seg.Audio.Duration(), Took: took})
			return
		}
		text = rest
	}
	bus.Publish(p.d.Bus, Transcribed, Transcript{Turn: id, Trigger: c.trig, Text: t.Text, Audio: c.seg.Audio.Duration(), Took: took})
	if p.cfg.Router.BreakGlass(c.trig, text) {
		p.stop(StopVoice)
		return
	}
	_ = p.handle(ctx, id, Utterance{Trigger: c.trig, Text: text, Ended: ended})
}

// handle routes a transcribed utterance and acts on it.
func (p *Pipeline) handle(ctx context.Context, id uint64, u Utterance) error {
	p.setState(id, Routing)
	r := p.cfg.Router.Route(u.Trigger, u.Text)
	bus.Publish(p.d.Bus, RouteChosen, Routed{Turn: id, Route: r})
	if u.Typed {
		ctx = context.WithValue(ctx, typedKey{}, true)
	}
	var err error
	out := Outcome{Turn: id, Trigger: u.Trigger, Route: r.Kind}
	switch r.Kind {
	case RouteDictation:
		err = p.dictate(ctx, id, r.Text, u.Ended)
	case RouteCommand:
		out.Handler, err = p.command(ctx, id, r.Text, u.Ended)
	}
	if err != nil {
		out.Err = err.Error()
	}
	bus.Publish(p.d.Bus, TurnDone, out)
	if err != nil && !errors.Is(err, context.Canceled) {
		return p.fail(id, err)
	}
	return err
}

func (p *Pipeline) dictate(ctx context.Context, id uint64, text string, ended time.Time) error {
	p.setState(id, Cleaning)
	in := p.d.Platform.Input()
	ed, _ := in.(platform.InputEditing)
	terminal := ed != nil && ed.FocusedTerminal(ctx)
	p.mu.Lock()
	code := terminal && !p.cfg.NoTerminal
	if p.codeMode != nil {
		code = *p.codeMode
	}
	p.mu.Unlock()
	scratchPrev := false
	if !code { // code mode drops fillers itself and keeps repeats ("dash dash")
		text = p.cfg.Cleaner.Clean(text)
	}
	if code || !p.cfg.NoCommands {
		r := p.formatter.Format(text, dictation.FormatState{Terminal: terminal, Code: code})
		text, scratchPrev = r.Text, r.ScratchPrev
		p.mu.Lock()
		if r.Code != code {
			p.codeMode = &r.Code
		}
		p.mu.Unlock()
	}
	if terminal {
		text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text) // never Enter in a terminal
	}
	if scratchPrev && ed != nil {
		p.mu.Lock()
		n := p.scratchLen
		p.scratchLen = 0
		p.mu.Unlock()
		if n > 0 {
			if err := ed.Backspace(ctx, n); err != nil {
				return fmt.Errorf("scratch that: %w", err)
			}
		}
	}
	if text == "" {
		return nil
	}
	p.setState(id, Inserting)
	start := time.Now()
	m := dictation.ChooseInsert(p.cfg.Insert, text, p.d.Layout, p.cfg.TypeMax)
	if err := dictation.Insert(ctx, in, m, text); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	ins := Insertion{Turn: id, Text: text, Method: m, Took: time.Since(start)}
	if !ended.IsZero() {
		ins.Latency = time.Since(ended)
	}
	p.mu.Lock()
	p.lastIns, p.lastInsAt = ins, time.Now()
	p.scratchLen = utf8.RuneCountInString(text)
	p.mu.Unlock()
	bus.Publish(p.d.Bus, Inserted, ins)
	return nil
}

// command acts on a command and says what handled it: a fast-path rule's
// name, HandlerAI, or HandlerNoAI.
func (p *Pipeline) command(ctx context.Context, id uint64, text string, ended time.Time) (string, error) {
	p.setState(id, Acknowledging)
	ack := p.playAck(ctx, id, ended)
	typed, _ := ctx.Value(typedKey{}).(bool)
	var sp *speaker
	if p.shouldSpeak(typed) {
		sp = p.newSpeaker(ctx, id)
		ctx = withSpeaker(ctx, sp)
	}
	reply, handler, err := p.act(ctx, id, text)
	<-ack // don't talk over the acknowledgement
	if err == nil && reply == "" {
		// It worked and there is nothing else to say.
		if reply = p.line(LineDone, ""); sp != nil {
			sp.streamedFinal = false
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		// Say the persona's error line; the caller still publishes the
		// failure.
		if l := p.line(LineError, ""); l != "" {
			if rerr := p.replyShown(ctx, id, l, l+"\n"+err.Error()); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return handler, err
		}
	}
	if err != nil || reply == "" {
		if sp != nil {
			sp.Abort()
		}
		return handler, err
	}
	return handler, p.reply(ctx, id, reply)
}

// act runs the fast path or the AI and returns what to say back, and what
// handled the command (see command).
func (p *Pipeline) act(ctx context.Context, id uint64, text string) (reply, handler string, err error) {
	match := stripFillers(text)
	for _, rule := range p.cfg.FastRules {
		run, ok := rule.Match(match)
		if !ok {
			continue
		}
		p.setState(id, FastPath)
		// Fast-path rules pass the permission gate like tools do; the
		// command text is what the user is asked about.
		spec := plugins.ToolSpec{Name: rule.Name, Permission: rule.Permission}
		args, _ := json.Marshal(map[string]string{"command": text})
		how, err := p.decide(ctx, spec, args, false)
		if err != nil {
			bus.Publish(p.d.Bus, FastPathDone, FastPathRan{Turn: id, Rule: rule.Name, Err: "refused: " + err.Error(), Confirmation: how})
			if cerr := ctx.Err(); cerr != nil {
				return "", rule.Name, cerr
			}
			return "I can't do that: " + err.Error() + ".", rule.Name, nil
		}
		reply, err := run(ctx, p)
		ev := FastPathRan{Turn: id, Rule: rule.Name, Reply: reply, Confirmation: how}
		if err != nil {
			ev.Err = err.Error()
		}
		bus.Publish(p.d.Bus, FastPathDone, ev)
		if err != nil {
			return "", rule.Name, fmt.Errorf("fast path %s: %w", rule.Name, err)
		}
		return reply, rule.Name, nil
	}
	if p.d.LLM == nil {
		return "Sorry, I can't do that without an AI provider.", HandlerNoAI, nil
	}
	reply, err = p.converse(ctx, id, text)
	return reply, HandlerAI, err
}

// AckSource supplies acknowledgement clips. Ack returns a random clip that
// is ready to play now, or false when there is none. It must not block: the
// acknowledgement exists to be instant.
type AckSource interface {
	Ack() (audio.Clip, bool)
}

type fixedAck audio.Clip

func (f fixedAck) Ack() (audio.Clip, bool) {
	c := audio.Clip(f)
	return c, len(c.Samples) > 0
}

// FixedAck is an AckSource that always returns c (none if c is empty).
func FixedAck(c audio.Clip) AckSource { return fixedAck(c) }

// playAck starts the acknowledgement clip and returns a channel closed when it
// has finished. When the first sample is handed to the player it publishes
// Acknowledged; ended is when the speech ended, if known.
func (p *Pipeline) playAck(ctx context.Context, id uint64, ended time.Time) <-chan struct{} {
	done := make(chan struct{})
	var clip audio.Clip
	ok := false
	if p.cfg.Ack != nil && !p.Quiet() {
		clip, ok = p.cfg.Ack.Ack()
	}
	if !ok || len(clip.Samples) == 0 {
		close(done)
		return done
	}
	began := time.Now()
	st := &ackStream{Stream: audio.NewClipStream(clip, 320), publish: func(at time.Time) {
		a := Acknowledgement{Turn: id, Clip: clip.Duration(), Lead: at.Sub(began)}
		if !ended.IsZero() {
			a.Latency = at.Sub(ended)
		}
		bus.Publish(p.d.Bus, Acknowledged, a)
	}}
	go func() {
		defer close(done)
		_ = p.d.Platform.Audio().Play(ctx, p.cfg.Speaker, st)
	}()
	return done
}

// ackStream reports the moment the player first reads a sample.
type ackStream struct {
	audio.Stream
	once    sync.Once
	publish func(time.Time)
}

func (s *ackStream) Recv() (audio.Frame, error) {
	f, err := s.Stream.Recv()
	if err == nil {
		s.once.Do(func() { s.publish(time.Now()) })
	}
	return f, err
}

// reply speaks text in the persona's voice and also shows it as a
// notification (replies.notify), or only shows it when the reply policy,
// quiet mode or quiet hours say not to speak. A reply the AI streamed has
// mostly been spoken already; this waits for the rest.
func (p *Pipeline) reply(ctx context.Context, id uint64, text string) error {
	return p.replyShown(ctx, id, text, text)
}

// replyShown is reply with a different text for the notification.
func (p *Pipeline) replyShown(ctx context.Context, id uint64, text, shown string) error {
	p.setState(id, Replying)
	bus.Publish(p.d.Bus, Replied, Reply{Turn: id, Text: text})
	sp := speakerFrom(ctx)
	if sp == nil || p.notifyReplies() {
		// The notification outlives the turn.
		_, err := p.d.Platform.Notifier().Notify(p.ctx, platform.Notification{Title: p.replyTitle(), Body: shown})
		if sp == nil {
			return err
		}
		// It is spoken too, so a notification that failed does not fail
		// the reply.
		sp.shown = true
	}
	if !sp.streamedFinal {
		sp.Break()
		sp.Feed(text)
	}
	return sp.Finish()
}

// Stop is the break-glass stop for the hotkey, the tray and the API
// (ADR-0017): it cancels every job and in-flight utterance (which stops any
// speech) and mutes the microphone until Unmute.
func (p *Pipeline) Stop() { p.stop(StopCall) }

func (p *Pipeline) stop(src StopSource) {
	p.mu.Lock()
	turns := len(p.turns)
	for id, cancel := range p.turns {
		cancel()
		delete(p.turns, id)
	}
	running := slices.Collect(maps.Values(p.jobs))
	ids := slices.Sorted(maps.Keys(p.jobs))
	// Forget the jobs now rather than when their watcher sees the final
	// update, so a later stop never reports a job this one already cancelled.
	clear(p.jobs)
	p.busy = 0
	p.clearArmLocked()
	if p.rec != nil {
		p.rec.cancel() // the audio so far is discarded
		p.rec = nil
	}
	p.muted = true
	p.pauseWakeLocked()
	p.setStateLocked(Idle, 0)
	p.mu.Unlock()

	for _, j := range running {
		j.Cancel()
	}
	p.setMic(platform.MicOff)
	bus.Publish(p.d.Bus, StopDone, Stopped{Source: src, Jobs: ids, Turns: turns})
}

// Muted reports whether the microphone is muted by a stop.
func (p *Pipeline) Muted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.muted
}

// Unmute reopens the microphone after a stop.
func (p *Pipeline) Unmute() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unmuteLocked()
}

func (p *Pipeline) unmuteLocked() {
	if p.muted {
		p.muted = false
		p.notifyLocked()
	}
}

// notifyLocked wakes the microphone loop to look at muted, wakeOff and rec
// again. p.mu must be held.
func (p *Pipeline) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// pauseWakeLocked closes the wake-listening microphone, if open, and wakes
// the loop to see why. p.mu must be held.
func (p *Pipeline) pauseWakeLocked() {
	if p.micCancel != nil {
		p.micCancel()
	}
	p.notifyLocked()
}

// SetWakeListening turns always-on wake-word listening on or off while the
// daemon runs (the CLI and API toggle). Off closes the microphone, leaving
// only push-to-talk; it has no effect without a wake-word engine.
func (p *Pipeline) SetWakeListening(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wakeOff == !on {
		return
	}
	p.wakeOff = !on
	if on {
		p.notifyLocked()
		return
	}
	p.clearArmLocked()
	p.pauseWakeLocked()
	if p.busy == 0 && p.rec == nil {
		p.setStateLocked(Idle, 0)
	}
}

// WakeListening reports whether the microphone is listening for the wake
// word: there is an engine and it has not been switched off.
func (p *Pipeline) WakeListening() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.d.Wake != nil && !p.wakeOff
}

// SetQuiet turns quiet mode on or off: no earcon or acknowledgement sound,
// and replies are shown as notifications rather than spoken.
func (p *Pipeline) SetQuiet(on bool) {
	p.mu.Lock()
	p.quiet = on
	p.mu.Unlock()
}

// Quiet reports whether quiet mode is on.
func (p *Pipeline) Quiet() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.quiet
}

func (p *Pipeline) setMic(s platform.TrayStatus) {
	_ = p.d.Platform.Notifier().SetTray(p.ctx, s)
	bus.Publish(p.d.Bus, MicChanged, s)
}
