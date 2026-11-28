package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/speech"
)

// TalkMode is how a push-to-talk hotkey works (input.dictate.mode and
// input.command.mode).
type TalkMode string

// Talk modes (ADR-0030).
const (
	// HoldToTalk records while the key is held; releasing it ends the
	// utterance. It is the default.
	HoldToTalk TalkMode = "hold"
	// TapToTalk starts recording on a tap; the VAD's end of speech, or a
	// second tap, ends the utterance.
	TapToTalk TalkMode = "toggle"
)

// Push-to-talk defaults (ADR-0030).
const (
	// DefaultReleaseTail is how long recording goes on after a hold-to-talk
	// key is released, so the last syllable, often still being spoken as
	// the key comes up, is not cut off.
	DefaultReleaseTail = 100 * time.Millisecond
	// DefaultMaxUtterance caps one push-to-talk recording.
	DefaultMaxUtterance = 2 * time.Minute
	// speechPad is kept around the speech the VAD found when the recording
	// is trimmed, so quiet word edges are not cut.
	speechPad = 500 * time.Millisecond
	// turnWait bounds how long a recorded utterance waits for the one before
	// it to be handled.
	turnWait = 30 * time.Second
)

// Wake-word command capture defaults.
const (
	// DefaultMinCommand is the least speech after a wake word that counts as
	// a command; the daemon uses it, so a lone wake word is dropped.
	DefaultMinCommand = 250 * time.Millisecond
	// DefaultMaxCommand caps the audio of one spoken command.
	DefaultMaxCommand = time.Minute
	// DefaultWakeWindow is how long a wake word waits for a command.
	DefaultWakeWindow = 8 * time.Second
	// wakePreRoll is kept from before the end of the wake word, because the
	// spotter reports it a little late and the first word after it may
	// already have started.
	wakePreRoll = 600 * time.Millisecond
	// wakeLookback is how far before the end of the wake word the audio the
	// second stage checks starts, at most (ADR-0041). It covers a slowly
	// said "hey footman" and the spotter's lateness.
	wakeLookback = 3 * time.Second
	// wakeRawHead is how much raw microphone audio before the end of the
	// wake word the second stage is given, when the wake word is not in a
	// speech segment of its own (ADR-0050). It covers a slowly said "hey
	// footman" and the spotter's lateness, without taking in much that was
	// said before it.
	wakeRawHead = 2 * time.Second
	// wakeGap is the silence put between a wake word said alone and the
	// command after it, when they are checked together.
	wakeGap = 300 * time.Millisecond
)

// ErrNoSpeech is published (as a Failure) when a push-to-talk recording held
// no speech, so nothing was transcribed.
var ErrNoSpeech = errors.New("no speech heard")

// recording is one push-to-talk utterance being captured.
type recording struct {
	trig   Trigger
	hotkey string
	mode   TalkMode
	cancel context.CancelFunc

	once     sync.Once
	released chan struct{} // closed by finish
	endedAt  time.Time     // when the key was released (or tapped again)
}

// finish ends the recording: a hold-to-talk key came up, or a tap-to-talk
// key was tapped again. at is when.
func (r *recording) finish(at time.Time) {
	r.once.Do(func() {
		r.endedAt = at
		close(r.released)
	})
}

// talkKey handles a press or release of a push-to-talk hotkey. A press starts
// a recording unless one is running; the matching release (hold) or the next
// press (tap) ends it. A press is the user asking, at the keyboard, to be
// heard, so it also lifts a mute left by a stop.
func (p *Pipeline) talkKey(ctx context.Context, recs *sync.WaitGroup, ev platform.HotkeyEvent, trig Trigger, mode TalkMode) {
	if mode == "" {
		mode = HoldToTalk
	}
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	p.mu.Lock()
	if r := p.rec; r != nil {
		if r.hotkey == ev.Hotkey && ev.Pressed == (r.mode == TapToTalk) {
			r.finish(at)
		}
		p.mu.Unlock()
		return
	}
	if !ev.Pressed {
		p.mu.Unlock()
		return
	}
	p.unmuteLocked()
	rctx, cancel := context.WithCancel(ctx)
	r := &recording{trig: trig, hotkey: ev.Hotkey, mode: mode, cancel: cancel, released: make(chan struct{})}
	p.rec = r
	p.pauseWakeLocked() // the microphone is the recording's alone
	if p.busy == 0 {
		p.setStateLocked(Listening, 0)
	}
	p.mu.Unlock()
	recs.Go(func() { p.record(ctx, rctx, r) })
}

// record captures one push-to-talk utterance from the microphone, which is
// open only for its duration, trims it to the speech the VAD found, and
// handles it. Recording ends when the key says so, the VAD hears the end of
// speech (tap to talk), MaxUtterance passes or the microphone goes away; a
// stop or ctx ending discards it.
func (p *Pipeline) record(ctx, rctx context.Context, r *recording) {
	handled := false
	defer func() {
		r.cancel()
		p.mu.Lock()
		if p.rec == r {
			p.rec = nil
			p.notifyLocked()
		}
		if !handled && p.busy == 0 && p.rec == nil && p.armed == 0 && p.state == Listening {
			p.setStateLocked(Idle, 0)
		}
		p.mu.Unlock()
	}()

	cctx, stopCapture := context.WithCancel(rctx)
	defer stopCapture()
	frames, err := p.d.Platform.Audio().Capture(cctx, p.cfg.Mic, audio.Speech)
	if err != nil {
		bus.Publish(p.d.Bus, Failed, Failure{State: Listening, Err: fmt.Sprintf("microphone: %v", err)})
		return
	}
	frames = p.level(cctx, frames)
	p.setMic(platform.Recording)
	// Keep speech-to-text loaded (loading it while the user speaks, if it
	// was unloaded) until this utterance is transcribed, so neither a reload
	// nor an idle unload falls between the key coming up and the text.
	if h, ok := p.d.STT.(interface{ Hold() func() }); ok {
		defer h.Hold()()
	}

	vadIn := make(chan audio.Frame, 256)
	segs := p.d.VAD.Detect(rctx, vadIn)
	var found []speech.Segment
	firstSeg := make(chan struct{})
	vadDone := make(chan struct{})
	go func() {
		defer close(vadDone)
		for s := range segs {
			found = append(found, s)
			if len(found) == 1 {
				close(firstSeg)
			}
		}
	}()

	var (
		samples  []float32
		base     time.Duration
		gotFirst bool
		tail     <-chan time.Time
		endSeg   <-chan struct{}
		released = r.released
	)
	if r.mode == TapToTalk {
		endSeg = firstSeg
	}
	limit := time.NewTimer(p.cfg.MaxUtterance)
	defer limit.Stop()
loop:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				break loop // the microphone went away
			}
			if !gotFirst {
				base, gotFirst = f.Offset, true
			}
			samples = append(samples, f.Samples...)
			select {
			case vadIn <- f:
			case <-rctx.Done():
				break loop
			}
		case <-released:
			released = nil
			tail = time.After(p.cfg.ReleaseTail)
		case <-tail:
			break loop
		case <-endSeg:
			r.finish(time.Now())
			break loop
		case <-limit.C:
			r.finish(time.Now())
			break loop
		case <-rctx.Done():
			break loop
		}
	}
	stopCapture()
	p.setMic(p.idleMic())
	close(vadIn)
	<-vadDone
	if rctx.Err() != nil {
		return // stopped, or shutting down: the audio is discarded
	}
	r.finish(time.Now()) // the microphone went away
	p.mu.Lock()
	if p.rec == r {
		p.rec = nil // the next press starts a new recording at once
		p.notifyLocked()
	}
	p.mu.Unlock()

	if len(found) == 0 {
		bus.Publish(p.d.Bus, Failed, Failure{State: Listening, Err: ErrNoSpeech.Error()})
		return
	}
	// Keep from just before the first speech to just after the last, so
	// silence (which speech-to-text may hallucinate on) is left out.
	idx := func(d time.Duration) int {
		return min(max(audio.Speech.Samples(d-base), 0), len(samples))
	}
	from, to := idx(found[0].Start-speechPad), idx(found[len(found)-1].End+speechPad)
	seg := speech.Segment{
		Start: found[0].Start, End: found[len(found)-1].End,
		Audio: audio.Clip{Format: audio.Speech, Samples: samples[from:to]},
	}
	handled = true
	p.handleRecorded(ctx, r, seg)
}

// handleRecorded transcribes and handles a push-to-talk utterance, after
// waiting (up to turnWait) for any utterance still being handled.
func (p *Pipeline) handleRecorded(ctx context.Context, r *recording, seg speech.Segment) {
	var id uint64
	var tctx context.Context
	deadline := time.NewTimer(turnWait)
	defer deadline.Stop()
	for {
		if id, tctx = p.beginTurn(ctx); id != 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			bus.Publish(p.d.Bus, Failed, Failure{State: Listening, Err: ErrBusy.Error()})
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer p.endTurn(id)
	p.transcribeAndHandle(tctx, id, claim{trig: r.trig, seg: seg}, r.endedAt)
}

// idleMic is the tray status when no push-to-talk recording is running.
func (p *Pipeline) idleMic() platform.TrayStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mic.pttOnly || p.wakeOff || p.muted || !p.mic.started || p.mic.ended {
		return platform.MicOff
	}
	if p.sess != nil {
		return platform.Recording // a dictation session keeps the microphone open
	}
	return platform.WakeListening
}
