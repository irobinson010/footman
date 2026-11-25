package core

import (
	"context"
	"errors"
	"sync"

	"github.com/irobinson010/footman/internal/audio"
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
// Microphone frames go to the wake-word spotter and the VAD. The wake word or
// a hotkey arms the pipeline (Listening); the VAD's next end of speech is then
// transcribed and handled. Speech that ends while nothing is armed is
// ignored, so nothing said without the wake word or a hotkey is ever
// transcribed. Hotkeys work on press; push-to-talk release is not used yet,
// so the VAD ends the utterance in both hold and toggle modes.
func (p *Pipeline) Run(ctx context.Context) error {
	if p.d.VAD == nil {
		return errors.New("core: Run needs a VAD")
	}
	p.mu.Lock()
	p.mic = micLoop{started: true}
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
)

// MicStatus is the microphone loop's state. Error says why it stopped.
type MicStatus struct {
	State MicState `json:"state"`
	Error string   `json:"error,omitempty"`
}

// micLoop records how Run went, for Mic.
type micLoop struct {
	started, ended bool
	err            error
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
	case p.muted:
		return MicStatus{State: MicMuted}
	}
	return MicStatus{State: MicOn}
}

// microphone is the body of Run, after the VAD check.
func (p *Pipeline) microphone(ctx context.Context) error {
	hctx, stopHotkeys := context.WithCancel(ctx)
	var hotkeys sync.WaitGroup
	defer func() {
		stopHotkeys()
		hotkeys.Wait()
	}()
	if combos := nonEmpty(p.cfg.DictateHotkey, p.cfg.CommandHotkey, p.cfg.StopHotkey); len(combos) > 0 {
		ch, err := p.d.Platform.Input().Hotkeys(hctx, combos)
		if err != nil {
			return err
		}
		hotkeys.Go(func() { p.hotkeyLoop(ch) })
	}

	var turns sync.WaitGroup
	defer turns.Wait()
	for {
		p.mu.Lock()
		muted, unmuted := p.muted, p.unmuted
		p.mu.Unlock()
		if muted {
			p.setMic(platform.MicOff)
			select {
			case <-unmuted:
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

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *Pipeline) hotkeyLoop(ch <-chan platform.HotkeyEvent) {
	for ev := range ch {
		if !ev.Pressed {
			continue
		}
		switch ev.Hotkey {
		case p.cfg.StopHotkey:
			p.stop(StopHotkey)
		case p.cfg.DictateHotkey:
			p.Arm(TriggerDictate)
		case p.cfg.CommandHotkey:
			p.Arm(TriggerCommand)
		}
	}
}

// listen runs one microphone session. It reports ended=true if the stream
// ended on its own rather than being muted or cancelled.
func (p *Pipeline) listen(ctx context.Context, turns *sync.WaitGroup) (ended bool, err error) {
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.mu.Lock()
	if p.muted {
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

	frames, err := p.d.Platform.Audio().Capture(mctx, p.cfg.Mic, audio.Speech)
	if err != nil {
		return false, err
	}
	p.setMic(platform.WakeListening)

	var wakes <-chan speech.WakeEvent
	vadIn := frames
	if p.d.Wake != nil {
		var wakeIn <-chan audio.Frame
		wakeIn, vadIn = tee(mctx, frames)
		wakes = p.d.Wake.Listen(mctx, wakeIn)
	}
	segs := p.d.VAD.Detect(mctx, vadIn)

	for segs != nil || wakes != nil {
		select {
		case _, ok := <-wakes:
			if !ok {
				wakes = nil
				continue
			}
			p.Arm(TriggerWake)
		case seg, ok := <-segs:
			if !ok {
				segs = nil
				continue
			}
			trig := p.takeArmed()
			if trig == 0 {
				continue // not addressed to Footman
			}
			turns.Go(func() { p.handleSegment(ctx, trig, seg) })
		}
	}
	return mctx.Err() == nil, nil
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
