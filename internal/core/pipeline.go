package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

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
	// Layout decides what auto insertion can type (default
	// dictation.ASCIILayout).
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
	// System is the LLM system prompt.
	System string
	// Model overrides the provider's default model.
	Model string
	// MaxToolRounds caps LLM round trips per command (default 8).
	MaxToolRounds int
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
	d   Deps
	cfg Config

	// ctx bounds background jobs; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // job watchers

	mu        sync.Mutex
	state     State
	turnSeq   uint64
	busy      uint64 // the turn being handled, or 0
	turns     map[uint64]context.CancelFunc
	armed     Trigger
	muted     bool
	unmuted   chan struct{} // closed by Unmute
	micCancel context.CancelFunc
	mic       micLoop
	jobSeq    int
	jobs      map[string]jobs.Job
}

// New builds a pipeline in the Idle state.
func New(d Deps, cfg Config) (*Pipeline, error) {
	if d.Bus == nil || d.Platform == nil || d.STT == nil {
		return nil, errors.New("core: Bus, Platform and STT are required")
	}
	if d.Layout == nil {
		d.Layout = dictation.ASCIILayout{}
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
	if cfg.TypeMax <= 0 {
		cfg.TypeMax = dictation.DefaultTypeMax
	}
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = 8
	}
	if cfg.Cleaner == nil {
		cfg.Cleaner = dictation.NewCleaner(nil, nil)
	}
	if cfg.Router == nil {
		cfg.Router = NewRouter(cfg.WakeWords, nil, nil)
	}
	if cfg.FastRules == nil {
		cfg.FastRules = DefaultFastRules(nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Pipeline{
		d: d, cfg: cfg, ctx: ctx, cancel: cancel,
		state: Idle,
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
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = trig
	if p.busy == 0 {
		p.setStateLocked(Listening, 0)
	}
}

func (p *Pipeline) takeArmed() Trigger {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.armed
	p.armed = 0
	return t
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
	p.setState(id, Transcribing)
	t, err := p.d.STT.Transcribe(tctx, seg.Audio)
	if err != nil {
		_ = p.fail(id, fmt.Errorf("transcribe: %w", err))
		return
	}
	bus.Publish(p.d.Bus, Transcribed, Transcript{Turn: id, Trigger: trig, Text: t.Text})
	if p.cfg.Router.BreakGlass(trig, t.Text) {
		p.stop(StopVoice)
		return
	}
	_ = p.handle(tctx, id, Utterance{Trigger: trig, Text: t.Text})
}

// handle routes a transcribed utterance and acts on it.
func (p *Pipeline) handle(ctx context.Context, id uint64, u Utterance) error {
	p.setState(id, Routing)
	r := p.cfg.Router.Route(u.Trigger, u.Text)
	bus.Publish(p.d.Bus, RouteChosen, Routed{Turn: id, Route: r})
	var err error
	switch r.Kind {
	case RouteDictation:
		err = p.dictate(ctx, id, r.Text)
	case RouteCommand:
		err = p.command(ctx, id, r.Text)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return p.fail(id, err)
	}
	return err
}

func (p *Pipeline) dictate(ctx context.Context, id uint64, text string) error {
	p.setState(id, Cleaning)
	text = p.cfg.Cleaner.Clean(text)
	if text == "" {
		return nil
	}
	p.setState(id, Inserting)
	m := dictation.ChooseInsert(p.cfg.Insert, text, p.d.Layout, p.cfg.TypeMax)
	if err := dictation.Insert(ctx, p.d.Platform.Input(), m, text); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	bus.Publish(p.d.Bus, Inserted, Insertion{Turn: id, Text: text, Method: m})
	return nil
}

func (p *Pipeline) command(ctx context.Context, id uint64, text string) error {
	p.setState(id, Acknowledging)
	ack := p.playAck(ctx)
	reply, err := p.act(ctx, id, text)
	<-ack // don't talk over the acknowledgement
	if err != nil {
		return err
	}
	if reply == "" {
		return nil
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
	if len(p.cfg.Ack.Samples) == 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		_ = p.d.Platform.Audio().Play(ctx, p.cfg.Speaker, audio.NewClipStream(p.cfg.Ack, 320))
	}()
	return done
}

// reply speaks text, or shows it as a notification when there is no TTS.
func (p *Pipeline) reply(ctx context.Context, id uint64, text string) error {
	p.setState(id, Replying)
	bus.Publish(p.d.Bus, Replied, Reply{Turn: id, Text: text})
	if p.d.TTS == nil {
		// The notification outlives the turn.
		_, err := p.d.Platform.Notifier().Notify(p.ctx, platform.Notification{Title: "Footman", Body: text})
		return err
	}
	s, err := p.d.TTS.Speak(ctx, text, p.cfg.Voice)
	if err != nil {
		return fmt.Errorf("speak: %w", err)
	}
	if err := p.d.Platform.Audio().Play(ctx, p.cfg.Speaker, s); err != nil {
		return fmt.Errorf("play: %w", err)
	}
	return nil
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
	p.busy = 0
	p.armed = 0
	if !p.muted {
		p.muted = true
		p.unmuted = make(chan struct{})
	}
	if p.micCancel != nil {
		p.micCancel()
	}
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
	if p.muted {
		p.muted = false
		close(p.unmuted)
	}
}

func (p *Pipeline) setMic(s platform.TrayStatus) {
	_ = p.d.Platform.Notifier().SetTray(p.ctx, s)
	bus.Publish(p.d.Bus, MicChanged, s)
}
