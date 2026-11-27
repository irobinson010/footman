package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/plugins"
	"github.com/irobinson010/footman/internal/speech"
)

// Voice-started dictation (ADR-0045). A fast-path rule starts a session; while
// it lasts, every VAD segment of the always-on microphone is transcribed and
// typed, with no wake word, until a stop phrase, the idle timeout or the
// break-glass stop.

// DictationConfig is the dictation session's settings (config dictation.*).
// Change it while running with SetDictation.
type DictationConfig struct {
	// NotesDir is the folder new notes go in (dictation.notes_dir), already
	// expanded. Empty means ~/Documents/Footman notes.
	NotesDir string
	// StopPhrases end a session when they are the whole utterance
	// (dictation.stop_phrases). Nil means DefaultDictationStopPhrases.
	StopPhrases []string
	// IdleTimeout ends a session after this long with no speech
	// (dictation.idle_timeout). Zero means DefaultDictationIdle; negative
	// means never.
	IdleTimeout time.Duration
}

// Dictation session defaults.
const (
	// DefaultDictationIdle is how long a session waits for speech.
	DefaultDictationIdle = 60 * time.Second
	// DefaultFocusTimeout is how long a new note's window may take to get
	// focus.
	DefaultFocusTimeout = 10 * time.Second
	// DefaultFocusSettle is the pause after the window has focus, so the
	// editor can take typing; four times it is the pause when the desktop
	// cannot say which window has focus.
	DefaultFocusSettle = 500 * time.Millisecond
	// DefaultFocusPoll is how often the active window is checked.
	DefaultFocusPoll = 200 * time.Millisecond
	// maxChoices is how many matching files are read out.
	maxChoices = 3
	// sessionQueue is how many utterances may wait to be transcribed.
	sessionQueue = 64
)

// FileFinder finds files by the name a person said (internal/filefind). It
// returns the best matches first.
type FileFinder interface {
	Find(ctx context.Context, name string) ([]string, error)
}

// Errors from dictation sessions.
var (
	// ErrNoSession is returned by ReplaceText and InsertAfterText when no
	// dictation session is running.
	ErrNoSession = errors.New("core: no dictation session is running")
	// errFocusTimeout means the window never took focus.
	errFocusTimeout = errors.New("the window did not take focus")
)

// SetDictation changes the dictation settings while running. A session in
// progress uses the new stop phrases and idle timeout from its next utterance.
func (p *Pipeline) SetDictation(c DictationConfig) {
	p.mu.Lock()
	p.cfg.Dictation = c
	p.mu.Unlock()
}

func (p *Pipeline) dictationConfig() DictationConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Dictation
}

func (p *Pipeline) idleTimeout() time.Duration {
	switch d := p.dictationConfig().IdleTimeout; {
	case d == 0:
		return DefaultDictationIdle
	case d < 0:
		return 0
	default:
		return d
	}
}

func (p *Pipeline) stopMatcher() stopMatcher {
	ps := p.dictationConfig().StopPhrases
	if ps == nil {
		ps = DefaultDictationStopPhrases
	}
	straight := make([]string, len(ps))
	for i, s := range ps {
		straight[i] = straightApostrophes.Replace(s)
	}
	return stopMatcher{r: p.cfg.Router, phrases: splitPhrases(straight)}
}

// notesDir is where new notes go.
func (p *Pipeline) notesDir() (string, error) {
	if d := p.dictationConfig().NotesDir; d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no notes folder: %w", err)
	}
	return filepath.Join(home, "Documents", "Footman notes"), nil
}

func (p *Pipeline) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}

// Dictation returns the current dictation session's status, with Active false
// when none is running.
func (p *Pipeline) Dictation() DictationStatus {
	p.mu.Lock()
	s := p.sess
	p.mu.Unlock()
	if s == nil {
		return DictationStatus{}
	}
	return s.status()
}

// dictSession is a running voice-started dictation session.
type dictSession struct {
	p      *Pipeline
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan speech.Segment
	done   chan struct{} // closed when run has returned

	mu      sync.Mutex // guards what follows
	closed  bool       // queue is closed
	reason  string     // why it ended: the first reason given
	phase   string
	target  string
	choices []string
}

// stopPhraseText is the stop phrase the status tells the user to say. It
// takes p.mu, so callers must not hold s.mu (which is taken under p.mu).
func (p *Pipeline) stopPhraseText() string {
	ps := p.dictationConfig().StopPhrases
	if ps == nil {
		ps = DefaultDictationStopPhrases
	}
	if len(ps) == 0 {
		return "stop dictation"
	}
	return ps[0]
}

func (s *dictSession) status() DictationStatus {
	phrase := s.p.stopPhraseText()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(phrase)
}

func (s *dictSession) statusLocked(phrase string) DictationStatus {
	st := DictationStatus{Active: true, Phase: s.phase, Target: s.target}
	switch s.phase {
	case PhaseChoosing:
		st.Message = "Which file? Say one, two or three, or cancel."
	default:
		st.Message = fmt.Sprintf("Dictating — say '%s'", phrase)
		if s.target != "" {
			st.Message = fmt.Sprintf("Dictating into %s — say '%s'", filepath.Base(s.target), phrase)
		}
	}
	return st
}

// end ends the session for reason (the first reason given wins). It returns at
// once; the session's goroutine winds down and reports the end.
func (s *dictSession) end(reason string) {
	s.mu.Lock()
	if s.reason == "" {
		s.reason = reason
	}
	s.mu.Unlock()
	s.cancel()
}

// push queues a segment for the session and reports whether it was taken. A
// session that cannot keep up drops the newest segment.
func (s *dictSession) push(seg speech.Segment) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.queue <- seg:
	default:
		bus.Publish(s.p.d.Bus, Failed, Failure{State: Listening, Err: "dictation is behind: an utterance was dropped"})
	}
	return true
}

// closeQueue lets the session finish what is queued, then end.
func (s *dictSession) closeQueue() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
}

// startSession begins a session in phase. It fails if one is running.
func (p *Pipeline) startSession(phase, target string, choices []string) (*dictSession, error) {
	p.mu.Lock()
	if p.sess != nil {
		p.mu.Unlock()
		return nil, errors.New("already dictating")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	s := &dictSession{
		p: p, ctx: ctx, cancel: cancel, queue: make(chan speech.Segment, sessionQueue),
		done: make(chan struct{}), phase: phase, target: target, choices: choices,
	}
	p.sess = s
	p.codeMode, p.scratchLen = nil, 0 // follow the focused app afresh
	// What was said while the start command was being handled belongs to
	// the session (the user did not wait for the acknowledgement).
	for _, seg := range p.late {
		s.push(seg)
	}
	p.holding, p.late = false, nil
	p.mu.Unlock()
	st := s.status()
	bus.Publish(p.d.Bus, DictationChanged, st)
	p.setMic(platform.Recording)
	p.show(st.Message)
	p.wg.Go(s.run)
	return s, nil
}

// closeSession ends the session, if any, for reason. With drain it first lets
// the utterances already queued be handled (the microphone stream ended).
func (p *Pipeline) closeSession(reason string, drain bool) {
	p.mu.Lock()
	s := p.sess
	p.mu.Unlock()
	if s == nil {
		return
	}
	if !drain {
		s.end(reason)
		return
	}
	s.closeQueue()
	select {
	case <-s.done:
	case <-time.After(turnWait):
		s.end(reason)
	}
}

// dictating reports whether a session is running.
func (p *Pipeline) dictating() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sess != nil
}

// finishSession reports the end of s and gives the microphone back.
func (p *Pipeline) finishSession(s *dictSession) {
	s.mu.Lock()
	reason := s.reason
	target := s.target
	s.mu.Unlock()
	if reason == "" {
		reason = EndShutdown
	}
	p.mu.Lock()
	if p.sess == s {
		p.sess = nil
	}
	p.mu.Unlock()
	s.cancel()
	msg := "Dictation stopped"
	switch reason {
	case EndIdle:
		msg += ": nothing said for a while"
	case EndStopped:
		msg += ": stopped"
	case EndMicrophone:
		msg += ": the microphone went away"
	case EndListening:
		msg += ": listening was switched off"
	}
	bus.Publish(p.d.Bus, DictationChanged, DictationStatus{Message: msg + ".", Target: target, Reason: reason})
	p.setMic(p.idleMic())
	if reason != EndShutdown {
		p.show(msg + ".")
		p.stopCue()
	}
}

// stopCue plays the persona's short sound for the end of a session, if the
// clip bank has one and quiet mode is off.
func (p *Pipeline) stopCue() {
	if p.cfg.StopCue == nil || p.Quiet() {
		return
	}
	clip, ok := p.cfg.StopCue.Ack()
	if !ok || len(clip.Samples) == 0 {
		return
	}
	go func() {
		_ = p.d.Platform.Audio().Play(p.ctx, p.cfg.Speaker, audio.NewClipStream(clip, 320))
	}()
}

// show puts text in a notification. Nothing is spoken: the microphone is open.
func (p *Pipeline) show(text string) {
	_, _ = p.d.Platform.Notifier().Notify(p.ctx, platform.Notification{Title: p.replyTitle(), Body: text})
}

// run is the session's goroutine.
func (s *dictSession) run() {
	p := s.p
	defer close(s.done)
	defer p.finishSession(s)
	// Keep speech-to-text loaded for the whole session.
	if h, ok := p.d.STT.(interface{ Hold() func() }); ok {
		defer h.Hold()()
	}
	var timer *time.Timer
	var timeout <-chan time.Time
	reset := func() {
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		d := p.idleTimeout()
		if d <= 0 {
			timeout = nil
			return
		}
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			timer.Reset(d)
		}
		timeout = timer.C
	}
	reset()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case seg, ok := <-s.queue:
			if !ok {
				s.end(EndMicrophone)
				return
			}
			reset() // speech heard: not idle
			if s.handle(seg) {
				return
			}
			reset()
		case <-timeout:
			s.end(EndIdle)
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// handle transcribes and acts on one utterance as a turn of its own, after
// waiting for any turn still running (the command that started the session).
// It reports whether the session is over.
func (s *dictSession) handle(seg speech.Segment) bool {
	p := s.p
	var id uint64
	var tctx context.Context
	deadline := time.NewTimer(turnWait)
	defer deadline.Stop()
	for {
		if id, tctx = p.beginTurn(s.ctx); id != 0 {
			break
		}
		select {
		case <-s.ctx.Done():
			return true
		case <-deadline.C:
			bus.Publish(p.d.Bus, Failed, Failure{State: Listening, Err: ErrBusy.Error()})
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer p.endTurn(id)
	ended := time.Now()
	p.setState(id, Transcribing)
	start := time.Now()
	t, err := p.d.STT.Transcribe(tctx, seg.Audio)
	if err != nil {
		if tctx.Err() == nil {
			_ = p.fail(id, fmt.Errorf("transcribe: %w", err))
		}
		return false
	}
	bus.Publish(p.d.Bus, Transcribed, Transcript{Turn: id, Trigger: TriggerDictate, Text: t.Text, Audio: seg.Audio.Duration(), Took: time.Since(start)})
	if len(dictation.NormWords(t.Text)) == 0 {
		return false
	}
	s.mu.Lock()
	phase := s.phase
	s.mu.Unlock()
	if phase == PhaseChoosing {
		return s.pick(tctx, id, t.Text)
	}
	return s.utterance(tctx, id, t.Text, ended)
}

// utterance acts on one transcribed utterance of a dictating session.
func (s *dictSession) utterance(ctx context.Context, id uint64, text string, ended time.Time) (over bool) {
	p := s.p
	out := Outcome{Turn: id, Trigger: TriggerDictate, Route: RouteDictation}
	defer func() { bus.Publish(p.d.Bus, TurnDone, out) }()

	// The break-glass phrase works here as everywhere, with the wake word.
	if after, ok := p.cfg.Router.MatchWake(text); ok && p.cfg.Router.BreakGlass(TriggerWake, after) {
		out.Handler = "break_glass"
		p.stop(StopVoice)
		return true
	}
	if rest, stop := p.stopMatcher().match(text); stop {
		out.Handler = "dictation_stop"
		if strings.TrimSpace(rest) != "" {
			bus.Publish(p.d.Bus, RouteChosen, Routed{Turn: id, Route: Route{Kind: RouteDictation, Text: rest}})
			if err := p.dictate(ctx, id, rest, ended); err != nil {
				out.Err = err.Error()
				_ = p.fail(id, err)
			}
		}
		s.end(EndStopPhrase)
		return true
	}
	if c, ok := matchEdit(dictation.NormWords(text)); ok {
		out.Route = RouteCommand
		out.Handler = s.edit(ctx, id, c, &out)
		return false
	}
	bus.Publish(p.d.Bus, RouteChosen, Routed{Turn: id, Route: Route{Kind: RouteDictation, Text: text}})
	if err := p.dictate(ctx, id, text, ended); err != nil {
		out.Err = err.Error()
		if !errors.Is(err, context.Canceled) {
			_ = p.fail(id, err)
		}
	}
	return false
}

// edit presses the keys of an editing command, unless a terminal has focus and
// the command is not safe there. It returns the outcome's handler name.
func (s *dictSession) edit(ctx context.Context, id uint64, c editCmd, out *Outcome) string {
	p := s.p
	in := p.d.Platform.Input()
	ed, _ := in.(platform.InputEditing)
	if !c.terminal && ed != nil && ed.FocusedTerminal(ctx) {
		p.show("Not sent: a terminal has focus, where Footman never presses Enter or sends editing shortcuts.")
		return "dictation_edit_blocked"
	}
	k, ok := in.(platform.InputKeys)
	if !ok {
		p.show("This desktop cannot press keys for Footman yet.")
		return "dictation_edit_unsupported"
	}
	p.setState(id, Inserting)
	for _, combo := range c.keys {
		if err := k.Key(ctx, combo); err != nil {
			out.Err = err.Error()
			if !errors.Is(err, context.Canceled) {
				_ = p.fail(id, fmt.Errorf("key %s: %w", combo, err))
			}
			return "dictation_edit"
		}
	}
	// The cursor moved or the text changed: "scratch that" must not take
	// back the last insertion any more.
	p.mu.Lock()
	p.scratchLen = 0
	p.mu.Unlock()
	return "dictation_edit"
}

// pick handles the answer to "which file?".
func (s *dictSession) pick(ctx context.Context, id uint64, text string) (over bool) {
	p := s.p
	out := Outcome{Turn: id, Trigger: TriggerDictate, Route: RouteCommand, Handler: "dictation_pick"}
	defer func() { bus.Publish(p.d.Bus, TurnDone, out) }()
	words := dictation.NormWords(text)
	if after, ok := p.cfg.Router.MatchWake(text); ok {
		if p.cfg.Router.BreakGlass(TriggerWake, after) {
			p.stop(StopVoice)
			return true
		}
		words = dictation.NormWords(after)
	}
	switch strings.Join(words, " ") {
	case "cancel", "never mind", "nevermind", "stop", "forget it", "stop dictation", "nothing":
		s.end(EndCancelled)
		return true
	}
	n, ok := pickNumber(words)
	s.mu.Lock()
	choices := s.choices
	s.mu.Unlock()
	if !ok || n >= len(choices) {
		return false // not an answer: keep waiting
	}
	path := choices[n]
	if err := p.openAndFocus(ctx, path); err != nil {
		out.Err = err.Error()
		p.show(openFailure(path, err))
		s.end(EndError)
		return true
	}
	phrase := p.stopPhraseText()
	s.mu.Lock()
	s.phase, s.target, s.choices = PhaseDictating, path, nil
	st := s.statusLocked(phrase)
	s.mu.Unlock()
	bus.Publish(p.d.Bus, DictationChanged, st)
	p.show(st.Message)
	return false
}

func openFailure(path string, err error) string {
	if errors.Is(err, errFocusTimeout) {
		return fmt.Sprintf("I opened %s but its window never took focus, so I have not started dictating. Click into it and say the wake word and \"write\".", filepath.Base(path))
	}
	return fmt.Sprintf("I couldn't open %s: %v", filepath.Base(path), err)
}

// ReplaceText replaces old with new in the focused document through the text
// editor seam (Deps.Editor), for the text-aware editing commands of a later
// design. It returns an error wrapping platform.ErrUnsupported until an editor
// is supplied, and ErrNoSession outside a dictation session.
func (p *Pipeline) ReplaceText(ctx context.Context, old, new string) error {
	if !p.dictating() {
		return ErrNoSession
	}
	return p.editor().Replace(ctx, old, new)
}

// InsertAfterText inserts text after anchor in the focused document through
// the text editor seam; see ReplaceText.
func (p *Pipeline) InsertAfterText(ctx context.Context, anchor, text string) error {
	if !p.dictating() {
		return ErrNoSession
	}
	return p.editor().InsertAfter(ctx, anchor, text)
}

func (p *Pipeline) editor() dictation.TextEditor {
	if p.d.Editor != nil {
		return p.d.Editor
	}
	return dictation.NoEditor
}

// silentKey carries, in a command's context, a flag a fast-path action sets
// when its work is done and nothing should be said (the microphone is open).
type silentKey struct{}

// silence tells command not to say the persona's "done" line.
func silence(ctx context.Context) {
	if f, ok := ctx.Value(silentKey{}).(*bool); ok {
		*f = true
	}
}

// DictationRule matches the phrases that start a dictation session, once the
// wake word has been removed: "write", "dictate", "take a note", "start
// dictation" (into the focused app), "write in a new note" and "write in a new
// document" (a new file), and "open <file> and write". Anything longer after
// "write" or "dictate" is still a one-shot dictation (the router sends it
// there). "stop dictation" with no session running only says so.
func DictationRule() FastRule {
	return FastRule{Name: "dictation", Permission: plugins.Act, Match: func(text string) (FastAction, bool) {
		spec, ok := matchStart(dictation.NormWords(text))
		if !ok {
			return nil, false
		}
		return func(ctx context.Context, p *Pipeline) (string, error) { return p.startDictation(ctx, spec) }, true
	}}
}

// startDictation runs a start phrase. It returns what to say, or nothing on
// success: the acknowledgement was the cue, and the status is shown.
func (p *Pipeline) startDictation(ctx context.Context, spec startSpec) (string, error) {
	if spec.kind == startStop {
		return "I'm not taking dictation.", nil
	}
	if p.d.Wake == nil {
		return "Dictating by voice needs the always-on microphone, which is not running.", nil
	}
	if p.dictating() {
		return "I'm already taking dictation.", nil
	}
	switch spec.kind {
	case startNewNote, startNewDoc:
		ext := ".txt"
		if spec.kind == startNewDoc {
			ext = ".md"
		}
		path, err := p.createNote(ext)
		if err != nil {
			return "", err
		}
		return p.openThenDictate(ctx, path)
	case startOpenFile:
		return p.openFileAndDictate(ctx, spec.file)
	}
	return p.beginDictating(ctx, "")
}

// beginDictating starts a dictating session for the focused app, or for
// target when it is a file.
func (p *Pipeline) beginDictating(ctx context.Context, target string) (string, error) {
	if _, err := p.startSession(PhaseDictating, target, nil); err != nil {
		return "I'm already taking dictation.", nil
	}
	silence(ctx)
	return "", nil
}

// createNote makes an empty, timestamped note file in the notes folder.
func (p *Pipeline) createNote(ext string) (string, error) {
	dir, err := p.notesDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create the notes folder: %w", err)
	}
	prefix := "note"
	if ext == ".md" {
		prefix = "document"
	}
	stamp := p.now().Format("2006-01-02 15.04.05")
	for i := 0; ; i++ {
		name := prefix + " " + stamp + ext
		if i > 0 {
			name = fmt.Sprintf("%s %s (%d)%s", prefix, stamp, i, ext)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create the note: %w", err)
		}
		return path, f.Close()
	}
}

// openThenDictate opens path in the default editor, waits for its window, and
// starts dictating into it.
func (p *Pipeline) openThenDictate(ctx context.Context, path string) (string, error) {
	if err := p.openAndFocus(ctx, path); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return openFailure(path, err), nil
	}
	return p.beginDictating(ctx, path)
}

// openFileAndDictate finds the file called name. One match is opened and
// dictated into; several are read out (the top three) and the session waits
// for the user to pick one by number.
func (p *Pipeline) openFileAndDictate(ctx context.Context, name string) (string, error) {
	if p.d.Finder == nil {
		return "I can't search for files here.", nil
	}
	found, err := p.d.Finder.Find(ctx, name)
	if err != nil {
		return "", fmt.Errorf("find %q: %w", name, err)
	}
	switch len(found) {
	case 0:
		return fmt.Sprintf("I couldn't find a file called %s.", name), nil
	case 1:
		return p.openThenDictate(ctx, found[0])
	}
	top := found[:min(len(found), maxChoices)]
	if _, err := p.startSession(PhaseChoosing, "", top); err != nil {
		return "I'm already taking dictation.", nil
	}
	var b strings.Builder
	if len(found) > len(top) {
		fmt.Fprintf(&b, "I found %d files called %s; the top three are: ", len(found), name)
	} else {
		fmt.Fprintf(&b, "I found %d files called %s: ", len(found), name)
	}
	for i, f := range top {
		fmt.Fprintf(&b, "%d, %s, in %s. ", i+1, filepath.Base(f), filepath.Base(filepath.Dir(f)))
	}
	b.WriteString("Which one?")
	return b.String(), nil
}

// openAndFocus opens path with the default application and waits (up to
// Config.FocusTimeout) for a window to take focus: a different window from the
// one that had focus before, or one whose title has the file's name. A desktop
// that cannot say which window has focus gets a longer fixed pause instead.
func (p *Pipeline) openAndFocus(ctx context.Context, path string) error {
	win := p.d.Platform.Windows()
	before, berr := win.Active(ctx)
	if err := p.d.Platform.Launcher().OpenFile(ctx, path); err != nil {
		return err
	}
	settle := p.cfg.FocusSettle
	if berr != nil {
		return sleepCtx(ctx, 4*settle)
	}
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	timeout := time.NewTimer(p.cfg.FocusTimeout)
	defer timeout.Stop()
	tick := time.NewTicker(p.cfg.FocusPoll)
	defer tick.Stop()
	for {
		if w, err := win.Active(ctx); err == nil && (w.ID != before.ID || strings.Contains(strings.ToLower(w.Title), stem)) {
			return sleepCtx(ctx, settle)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errFocusTimeout
		case <-tick.C:
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
