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
