package core

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/speech"
)

// Run drives the pipeline from the microphone and the global hotkeys until ctx
// is done (it then returns ctx's error) or the microphone stream ends on its
// own, as when the device goes away (it then returns nil once the utterances
// in flight are handled, unless one of them stopped everything). While muted
// by a stop it keeps serving the hotkeys and reopens the microphone on
// Unmute.
//
// The dictation and command hotkeys are push-to-talk (ADR-0030): a press
// opens the microphone for that utterance alone, and the release (hold to
// talk) or the VAD's end of speech or a second tap (tap to talk) ends it.
//
// With a wake-word engine, the microphone is also open the whole time:
// frames go to the wake-word spotter and the VAD (the tray shows "listening
// for the wake word"). The wake word (or Arm) arms the pipeline (Listening),
// and the VAD's next end of speech is then cut to the command (a short
// pre-roll before the end of the wake word, at most MaxCommand), transcribed
// and handled. A wake word nothing follows within WakeWindow is dropped
// silently. Speech that ends while nothing is armed is ignored, so nothing
// said without the wake word or a hotkey is ever transcribed. While a
// push-to-talk recording runs, or after SetWakeListening(false), the wake
// microphone is closed, so the device is never opened twice. Without an
// engine, the microphone is closed except while a hotkey records, and Run
// returns only when ctx is done.
func (p *Pipeline) Run(ctx context.Context) error {
	if p.d.VAD == nil {
		return errors.New("core: Run needs a VAD")
	}
	p.mu.Lock()
	p.mic = micLoop{started: true, pttOnly: p.d.Wake == nil}
	p.mu.Unlock()
	err := p.microphone(ctx)
	p.mu.Lock()
	p.mic.ended = true
	p.mic.err = err
	p.mu.Unlock()
	return err
}

// MicState is what the microphone loop is doing (see Pipeline.Mic).
type MicState string

// Microphone loop states.
const (
	// MicOff means Run has not been started, so the microphone is not used.
	MicOff MicState = "off"
	// MicOn means the microphone loop is running.
	MicOn MicState = "on"
	// MicMuted means the loop is running but muted by a stop.
	MicMuted MicState = "muted"
	// MicStopped means the loop has returned and will not listen again.
	MicStopped MicState = "stopped"
	// MicPushToTalk means there is no wake word, or it was switched off: the
	// microphone is closed and opens only while a push-to-talk hotkey
	// records.
	MicPushToTalk MicState = "push_to_talk"
	// MicRecording means a push-to-talk hotkey is recording right now.
	MicRecording MicState = "recording"
)

// MicStatus is the microphone loop's state. Error says why it stopped.
type MicStatus struct {
	State MicState `json:"state"`
	Error string   `json:"error,omitempty"`
}

// micLoop records how Run went, for Mic.
type micLoop struct {
	started, ended bool
	// pttOnly means there is no wake-word loop: the microphone opens only
	// for push-to-talk.
	pttOnly bool
	err     error
}

// Mic reports what the microphone loop is doing: whether it is on, muted by a
// stop, not started, or stopped, with the error that stopped it. A loop that
// ended without an error (the device went away) is stopped too.
func (p *Pipeline) Mic() MicStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.mic.ended:
		msg := "the microphone stream ended"
		if p.mic.err != nil {
			msg = p.mic.err.Error()
		}
		return MicStatus{State: MicStopped, Error: msg}
	case !p.mic.started:
		return MicStatus{State: MicOff}
	case p.rec != nil:
		return MicStatus{State: MicRecording}
	case p.muted:
		return MicStatus{State: MicMuted}
	case p.mic.pttOnly || p.wakeOff:
		return MicStatus{State: MicPushToTalk}
	}
	return MicStatus{State: MicOn}
}

// microphone is the body of Run, after the VAD check.
func (p *Pipeline) microphone(ctx context.Context) error {
	hctx, stopHotkeys := context.WithCancel(ctx)
	var hotkeys, recs sync.WaitGroup
	defer func() {
		stopHotkeys()
		hotkeys.Wait()
		recs.Wait()
	}()
	// A dictation session cannot outlive the microphone, but first types
	// what was already heard.
	defer func() { p.closeSession(EndMicrophone, ctx.Err() == nil) }()
	if combos := nonEmpty(p.cfg.DictateHotkey, p.cfg.CommandHotkey, p.cfg.StopHotkey); len(combos) > 0 {
		chans, err := p.registerHotkeys(hctx, combos)
		if err != nil {
			return err
		}
		for _, ch := range chans {
			hotkeys.Go(func() { p.hotkeyLoop(hctx, &recs, ch) })
		}
	}

	if p.d.Wake == nil {
		// Push-to-talk only: nothing listens until a hotkey is pressed.
		p.setMic(platform.MicOff)
		<-ctx.Done()
		return ctx.Err()
	}

	var turns sync.WaitGroup
	defer turns.Wait()
	for {
		p.mu.Lock()
		muted, off, recording, changed := p.muted, p.wakeOff, p.rec != nil, p.changed
		p.mu.Unlock()
		if muted || off || recording {
			if !recording { // a recording shows its own status
				p.setMic(platform.MicOff)
			}
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		ended, err := p.listen(ctx, &turns)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ended {
			// Let the last utterances finish; if one of them was a stop, stay
			// up muted for the hotkeys rather than returning.
			turns.Wait()
			if !p.Muted() {
				return nil
			}
		}
	}
}

// registerHotkeys registers combos together, or, if that fails, each on its
// own, so that a key another app holds (say the command key) does not take
// dictation down with it. It fails only if none can be registered; the keys
// that failed are reported by HotkeyErrors and published as failures.
func (p *Pipeline) registerHotkeys(ctx context.Context, combos []string) ([]<-chan platform.HotkeyEvent, error) {
	in := p.d.Platform.Input()
	ch, err := in.Hotkeys(ctx, combos)
	if err == nil || len(combos) == 1 {
		if err != nil {
			return nil, err
		}
		return []<-chan platform.HotkeyEvent{ch}, nil
	}
	var chans []<-chan platform.HotkeyEvent
	failed := map[string]string{}
	for _, c := range combos {
		ch, cerr := in.Hotkeys(ctx, []string{c})
		if cerr != nil {
			failed[c] = cerr.Error()
			continue
		}
		chans = append(chans, ch)
	}
	if len(chans) == 0 {
		return nil, err
	}
	p.mu.Lock()
	p.hotkeyErrs = failed
	p.mu.Unlock()
	for _, c := range combos {
		if msg, ok := failed[c]; ok {
			bus.Publish(p.d.Bus, Failed, Failure{State: Idle, Err: "hotkey " + c + ": " + msg})
		}
	}
	return chans, nil
}

// HotkeyErrors returns, for each hotkey that could not be registered, why.
func (p *Pipeline) HotkeyErrors() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.hotkeyErrs)
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *Pipeline) hotkeyLoop(ctx context.Context, recs *sync.WaitGroup, ch <-chan platform.HotkeyEvent) {
	for ev := range ch {
		switch ev.Hotkey {
		case p.cfg.StopHotkey:
			if ev.Pressed {
				p.stop(StopHotkey)
			}
		case p.cfg.DictateHotkey:
			p.talkKey(ctx, recs, ev, TriggerDictate, p.cfg.DictateMode)
		case p.cfg.CommandHotkey:
			p.talkKey(ctx, recs, ev, TriggerCommand, p.cfg.CommandMode)
		}
	}
}

// listen runs one microphone session. It reports ended=true if the stream
// ended on its own rather than being muted or cancelled.
func (p *Pipeline) listen(ctx context.Context, turns *sync.WaitGroup) (ended bool, err error) {
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.mu.Lock()
	if p.muted || p.wakeOff || p.rec != nil {
		p.mu.Unlock()
		return false, nil
	}
	p.micCancel = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.micCancel = nil
		p.mu.Unlock()
	}()

	defer p.disarm()        // a wake word nothing followed before the stream ended
	defer p.closeFollowUp() // and a follow-up window has no microphone without it
	frames, err := p.d.Platform.Audio().Capture(mctx, p.cfg.Mic, audio.Speech)
	if err != nil {
		return false, err
	}
	frames = p.trackCapture(mctx, frames)
	p.setMic(p.idleMic())
	frames = p.level(mctx, frames)

	var wakes <-chan speech.WakeEvent
	vadIn := frames
	if p.d.Wake != nil {
		var wakeIn <-chan audio.Frame
		wakeIn, vadIn = tee(mctx, frames)
		wakes = p.d.Wake.Listen(mctx, wakeIn)
	}
	segs := p.d.VAD.Detect(mctx, vadIn)

	// A wake word that no command follows is dropped when this fires.
	var expire <-chan time.Time
	for segs != nil || wakes != nil {
		select {
		case <-expire:
			expire = nil
			p.disarm()
		case w, ok := <-wakes:
			if !ok {
				wakes = nil
				continue
			}
			if w.Kind == speech.WakeKindBreakGlass {
				// The spotter heard the stop phrase right after a wake
				// word (ADR-0017): stop at once, without waiting for the
				// utterance to be transcribed.
				p.stop(StopVoice)
				continue
			}
			if p.dictating() {
				continue // a dictation session needs no wake word (ADR-0045)
			}
			bus.Publish(p.d.Bus, WakeHeard, Wake{Keyword: w.Keyword, At: w.At})
			p.arm(TriggerWake, w.At, true)
			expire = time.After(p.cfg.WakeWindow)
			p.earcon(mctx)
		case seg, ok := <-segs:
			if !ok {
				segs = nil
				continue
			}
			segAt := time.Now()
			c, ok := p.claimSegment(seg)
			if !ok {
				continue // not addressed to Footman, or only the wake word
			}
			expire = nil
			turns.Go(func() { p.handleSegment(ctx, c, segAt) })
		}
	}
	return mctx.Err() == nil, nil
}

// earcon plays the short wake sound, unless there is none or quiet mode is on.
func (p *Pipeline) earcon(ctx context.Context) {
	if len(p.cfg.Earcon.Samples) == 0 || p.Quiet() {
		return
	}
	go func() {
		_ = p.d.Platform.Audio().Play(ctx, p.cfg.Speaker, audio.NewClipStream(p.cfg.Earcon, 320))
	}()
}

// tee copies every frame to two unbuffered channels, the wake-word spotter's
// first. Because the copy to the spotter blocks until it is taken, a wake
// word is always reported before the VAD sees the frames after it, so the
// utterance that follows is never mistaken for unaddressed speech.
func tee(ctx context.Context, in <-chan audio.Frame) (first, second <-chan audio.Frame) {
	a, b := make(chan audio.Frame), make(chan audio.Frame)
	go func() {
		defer close(a)
		defer close(b)
		for f := range in {
			for _, ch := range []chan audio.Frame{a, b} {
				select {
				case ch <- f:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return a, b
}

// level applies the level normaliser (ADR-0035) to captured frames when it is
// on. Each capture gets its own, so gain never carries between sessions.
func (p *Pipeline) level(ctx context.Context, in <-chan audio.Frame) <-chan audio.Frame {
	if !p.cfg.AGC {
		return in
	}
	return audio.NewAGC(audio.AGCConfig{MaxGainDB: p.cfg.AGCMaxGainDB}).Stream(ctx, in)
}
