package core

import (
	"context"
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
	// Ack is the persona's acknowledgement clip, played as a command starts.
	// Empty means none.
	Ack audio.Clip
	// Voice is the TTS voice for replies.
	Voice speech.Voice
	// Speak is the reply policy (config replies.speak): "always",
	// "when-voice" (the default: only for requests that came by voice) or
	// "never". Change it while running with SetReplyPolicy.
	Speak string
	// QuietHours is a range like "22:00-07:00" with no spoken replies (config
	// replies.quiet_hours). Empty means none.
	QuietHours string
	// Now is the clock for quiet hours (default time.Now).
	Now func() time.Time
	// System is the LLM system prompt.
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
		turns: map[uint64]context.CancelFunc{},
		jobs:  map[string]jobs.Job{},
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
	p.armed, p.armedTimed = 0, false
	if p.busy == 0 && p.rec == nil {
		p.setStateLocked(Idle, 0)
	}
}

// claimSegment hands seg to the armed trigger. For a wake word it first cuts
// the audio to the command: from a pre-roll before the end of the wake word
// (so the first word after it is not clipped, nor the wake word kept), to at
// most MaxCommand. A segment that ended before the wake word, or has less
// than MinCommand left after it (the wake word itself, said alone), is not
// claimed and the wake stays armed for the next one. ok is false then, or
// when nothing is armed.
func (p *Pipeline) claimSegment(seg speech.Segment) (trig Trigger, out speech.Segment, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.armed == 0 {
		return 0, seg, false
	}
	if p.armedTimed {
		cut := p.armedAt - wakePreRoll
		if seg.End <= p.armedAt {
			return 0, seg, false
		}
		if from := audio.Speech.Samples(cut - seg.Start); from > 0 {
			seg.Audio.Samples = seg.Audio.Samples[min(from, len(seg.Audio.Samples)):]
			seg.Start = max(seg.Start, cut)
		}
		if seg.Audio.Duration() < max(p.cfg.MinCommand, time.Millisecond) {
			return 0, seg, false
		}
		if n := audio.Speech.Samples(p.cfg.MaxCommand); len(seg.Audio.Samples) > n {
			seg.Audio.Samples = seg.Audio.Samples[:n]
		}
	}
	trig = p.armed
	p.armed, p.armedTimed = 0, false
	return trig, seg, true
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
func (p *Pipeline) handleSegment(ctx context.Context, trig Trigger, seg speech.Segment) {
	id, tctx := p.beginTurn(ctx)
	if id == 0 {
		// Busy: only the break-glass stop can get through (ADR-0017).
		if trig == TriggerWake {
			if t, err := p.d.STT.Transcribe(ctx, seg.Audio); err == nil && p.cfg.Router.BreakGlass(trig, t.Text) {
				p.stop(StopVoice)
			}
		}
		return
	}
	defer p.endTurn(id)
	p.transcribeAndHandle(tctx, id, trig, seg, time.Time{})
}

// transcribeAndHandle transcribes seg for turn id and handles the text.
// ended is when the speech ended, if known.
func (p *Pipeline) transcribeAndHandle(ctx context.Context, id uint64, trig Trigger, seg speech.Segment, ended time.Time) {
	p.setState(id, Transcribing)
	start := time.Now()
	t, err := p.d.STT.Transcribe(ctx, seg.Audio)
	if err != nil {
		_ = p.fail(id, fmt.Errorf("transcribe: %w", err))
		return
	}
	bus.Publish(p.d.Bus, Transcribed, Transcript{Turn: id, Trigger: trig, Text: t.Text, Audio: seg.Audio.Duration(), Took: time.Since(start)})
	if p.cfg.Router.BreakGlass(trig, t.Text) {
		p.stop(StopVoice)
		return
	}
	_ = p.handle(ctx, id, Utterance{Trigger: trig, Text: t.Text, Ended: ended})
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
	switch r.Kind {
	case RouteDictation:
		err = p.dictate(ctx, id, r.Text, u.Ended)
	case RouteCommand:
		err = p.command(ctx, id, r.Text)
	}
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

func (p *Pipeline) command(ctx context.Context, id uint64, text string) error {
	p.setState(id, Acknowledging)
	ack := p.playAck(ctx)
	typed, _ := ctx.Value(typedKey{}).(bool)
	var sp *speaker
	if p.shouldSpeak(typed) {
		sp = p.newSpeaker(ctx, id)
		ctx = withSpeaker(ctx, sp)
	}
	reply, err := p.act(ctx, id, text)
	<-ack // don't talk over the acknowledgement
	if err != nil || reply == "" {
		if sp != nil {
			sp.Abort()
		}
		return err
	}
	return p.reply(ctx, id, reply)
}

// act runs the fast path or the AI and returns what to say back.
func (p *Pipeline) act(ctx context.Context, id uint64, text string) (string, error) {
	for _, rule := range p.cfg.FastRules {
		run, ok := rule.Match(text)
		if !ok {
			continue
		}
		p.setState(id, FastPath)
		reply, err := run(ctx, p)
		ev := FastPathRan{Turn: id, Rule: rule.Name}
		if err != nil {
			ev.Err = err.Error()
		}
		bus.Publish(p.d.Bus, FastPathDone, ev)
		if err != nil {
			return "", fmt.Errorf("fast path %s: %w", rule.Name, err)
		}
		return reply, nil
	}
	if p.d.LLM == nil {
		return "Sorry, I can't do that without an AI provider.", nil
	}
	return p.converse(ctx, id, text)
}

// playAck starts the acknowledgement clip and returns a channel closed when it
// has finished.
func (p *Pipeline) playAck(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if len(p.cfg.Ack.Samples) == 0 || p.Quiet() {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		_ = p.d.Platform.Audio().Play(ctx, p.cfg.Speaker, audio.NewClipStream(p.cfg.Ack, 320))
	}()
	return done
}

// reply speaks text, or shows it as a notification when the reply policy or
// quiet mode says not to speak. A reply the AI streamed has mostly been spoken
// already; this waits for the rest.
func (p *Pipeline) reply(ctx context.Context, id uint64, text string) error {
	p.setState(id, Replying)
	bus.Publish(p.d.Bus, Replied, Reply{Turn: id, Text: text})
	sp := speakerFrom(ctx)
	if sp == nil {
		// The notification outlives the turn.
		_, err := p.d.Platform.Notifier().Notify(p.ctx, platform.Notification{Title: "Footman", Body: text})
		return err
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
	p.armed = 0
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
	p.armed, p.armedTimed = 0, false
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
