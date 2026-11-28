package core_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/speech"
)

// warmTTS counts how often the pipeline got the speech engine ready.
type warmTTS struct {
	speech.TTS
	warms *atomic.Int32
}

func (w warmTTS) Warm(context.Context) { w.warms.Add(1) }

func awaitCount(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("count = %d, want %d", n.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// The wake word starts getting the speech engine and the AI ready, so a
// model unloaded for being idle is back, and the connection open, by the end
// of speech (ADR-0051).
func TestWakeWordWarmsSpeechAndAI(t *testing.T) {
	var warms atomic.Int32
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.TTS = warmTTS{TTS: d.TTS, warms: &warms} })
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("open youtube")
	r.run()
	awaitCount(t, &warms, 1)
	deadline := time.Now().Add(2 * time.Second)
	for r.ai.Warms() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the AI was not warmed")
		}
		time.Sleep(time.Millisecond)
	}
}

// Nothing is spoken in quiet mode or when replies are never spoken, so the
// speech engine stays unloaded.
func TestWarmSkipsSpeechWhenNothingIsSpoken(t *testing.T) {
	var warms atomic.Int32
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		d.TTS = warmTTS{TTS: d.TTS, warms: &warms}
		c.Speak = core.SpeakNever
	})
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("open youtube")
	r.run()
	time.Sleep(50 * time.Millisecond)
	if warms.Load() != 0 {
		t.Errorf("speech engine warmed %d times with replies off", warms.Load())
	}
}

// The command hotkey warms too; the dictation hotkey does not, as it never
// speaks or asks the AI.
func TestCommandHotkeyWarmsButDictationDoesNot(t *testing.T) {
	var warms atomic.Int32
	r := newPTT(t, func(d *core.Deps, _ *core.Config) { d.TTS = warmTTS{TTS: d.TTS, warms: &warms} })
	r.say(3)
	r.transcript("hello")
	r.input().Press("F8", true) // dictation
	r.awaitMic(platform.Recording)
	time.Sleep(20 * time.Millisecond)
	if warms.Load() != 0 {
		t.Fatalf("dictation warmed the speech engine %d times", warms.Load())
	}
	r.end("F8", false)
	r.awaitMic(platform.MicOff)

	r.say(3)
	r.transcript("open youtube")
	r.input().Press("Shift+F8", true)
	r.awaitMic(platform.Recording)
	awaitCount(t, &warms, 1)
	r.end("Shift+F8", false)
	r.awaitMic(platform.MicOff)
}
