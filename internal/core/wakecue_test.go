package core_test

import (
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/platform"
)

// Distinct lengths tell the clips apart in the fake speaker: the rig's
// acknowledgement has 2 samples.
var (
	testEarcon  = audio.Clip{Format: audio.Speech, Samples: []float32{0.3, 0.3, 0.3}}
	testMissCue = audio.Clip{Format: audio.Speech, Samples: []float32{0.2, 0.2, 0.2, 0.2}}
)

// played counts the clips of each length the fake speaker has played.
func (r *rig) played() map[int]int {
	time.Sleep(30 * time.Millisecond) // cues play in the background
	out := map[int]int{}
	for _, c := range r.speaker().Played() {
		out[len(c.Samples)]++
	}
	return out
}

// SetEarcon switches the wake earcon off and on while running.
func TestSetEarcon(t *testing.T) {
	for _, on := range []bool{false, true} {
		r := newRig(t, func(_ *core.Deps, c *core.Config) { c.Earcon = testEarcon })
		r.p.SetEarcon(on)
		r.wake.Schedule(0)
		r.say(3)
		r.transcript("open youtube")
		r.run()
		want := 0
		if on {
			want = 1
		}
		if got := r.played()[3]; got != want {
			t.Errorf("earcon on=%v: played %d times, want %d", on, got, want)
		}
	}
}

// A wake that the second stage rejects because speech was heard that is not
// a wake word plays the softer miss cue; silence (no speech heard) does not,
// nor does quiet mode or switching the earcon off.
func TestMissCue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		quiet bool
		off   bool
		want  int
	}{
		{"speech that is not a wake word", "and then we open the door", false, false, 1},
		{"no speech", "", false, false, 0},
		{"quiet mode", "and then we open the door", true, false, 0},
		{"earcon off", "and then we open the door", false, true, 0},
		{"confirmed wake", "Alfred, open YouTube", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, func(_ *core.Deps, c *core.Config) {
				c.VerifyWake = true
				c.WakeWords = []string{"alfred"}
				c.MinCommand = 100 * time.Millisecond
				c.Earcon, c.MissCue, c.Quiet = testEarcon, testMissCue, tc.quiet
			})
			r.p.SetEarcon(!tc.off)
			r.speak(repeat("x", 30) + ".")
			r.wake.Schedule(10)
			r.transcript(tc.text)
			r.run()
			if got := r.played()[4]; got != tc.want {
				t.Errorf("miss cue played %d times, want %d", got, tc.want)
			}
		})
	}
}

// The transcript of a rejected wake is in the report only with WakeDebug.
func TestWakeDebugText(t *testing.T) {
	for _, debug := range []bool{false, true} {
		r := newRig(t, func(_ *core.Deps, c *core.Config) {
			c.VerifyWake = true
			c.WakeWords = []string{"alfred"}
			c.MinCommand = 100 * time.Millisecond
		})
		r.p.SetWakeDebug(debug)
		rejected := bus.Subscribe(r.bus, core.WakeRejected, 4)
		r.speak(repeat("x", 30) + ".")
		r.wake.Schedule(10)
		r.transcript("and then we open the door")
		r.run()
		got := await(t, rejected, func(core.WakeRejection) bool { return true })
		want := ""
		if debug {
			want = "and then we open the door"
		}
		if got.Text != want || got.Reason != core.WakeNoMatch {
			t.Errorf("debug=%v: rejection %+v, want text %q", debug, got, want)
		}
	}
}

// The tray shows the microphone as recording from the wake word until the
// command has been captured, then goes back to listening for the wake word.
func TestWakeShowsRecordingInTray(t *testing.T) {
	r := newRig(t, nil)
	mic := bus.Subscribe(r.bus, core.MicChanged, 64)
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("open youtube")
	r.run()
	var got []platform.TrayStatus
	for {
		select {
		case ev := <-mic.C():
			got = append(got, ev.Data)
			continue
		default:
		}
		break
	}
	want := []platform.TrayStatus{platform.WakeListening, platform.Recording, platform.WakeListening}
	if len(got) < 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("tray went %v, want it to start %v", got, want)
	}
}

// A spoken command reports where its time went: speech-to-text, the AI's
// first token and the first audio of the reply, all counted from the end of
// speech, and the wake word to the end of speech. Typed text has none.
func TestTurnTiming(t *testing.T) {
	r := newRig(t, nil)
	timings := bus.Subscribe(r.bus, core.Timing, 4)
	r.ai.Queue(lfake.Reply{Text: "Very good, sir."})
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("how are you")
	r.run()
	got := await(t, timings, func(core.TurnTiming) bool { return true })
	if got.Trigger != core.TriggerWake || got.Transcribe <= 0 || got.FirstToken <= 0 || got.FirstAudio <= 0 {
		t.Errorf("timing %+v, want a wake turn with transcribe, first token and first audio", got)
	}
	if got.FirstAudio < got.FirstToken {
		t.Errorf("first audio %v before the first token %v", got.FirstAudio, got.FirstToken)
	}
	if got.Ack <= 0 {
		t.Errorf("no acknowledgement time: %+v", got)
	}

	r2 := newRig(t, nil)
	timings2 := bus.Subscribe(r2.bus, core.Timing, 4)
	r2.ai.Queue(lfake.Reply{Text: "Fine."})
	if err := r2.p.Submit(r2.ctx(), core.Utterance{Trigger: core.TriggerCommand, Text: "hello", Typed: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-timings2.C():
		t.Errorf("typed text reported timing %+v", ev.Data)
	default:
	}
}
