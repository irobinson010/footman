package permissions_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/permissions"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/plugins"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	sfake "github.com/irobinson010/footman/internal/speech/fake"
)

// These tests run the real pipeline, gate and broker on fakes: Footman asks
// aloud, the answer comes back through the follow-up window (ADR-0047).

const guardTime = 10 * time.Second

// levelTool is a tool of a given permission level that counts its runs.
type levelTool struct {
	name  string
	level plugins.Permission
	mu    sync.Mutex
	ran   int
}

func (t *levelTool) Spec() plugins.ToolSpec {
	return plugins.ToolSpec{Name: t.name, Description: "Does it.", Schema: json.RawMessage(`{"type":"object"}`), Permission: t.level}
}

func (t *levelTool) Run(context.Context, json.RawMessage) (plugins.Result, error) {
	t.mu.Lock()
	t.ran++
	t.mu.Unlock()
	return plugins.Result{Text: "done"}, nil
}

func (t *levelTool) runs() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ran
}

type timers struct {
	mu    sync.Mutex
	chans []chan time.Time
}

func (f *timers) After(time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.chans = append(f.chans, ch)
	return ch
}

func (f *timers) fireLast() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chans[len(f.chans)-1] <- time.Time{}
}

func (f *timers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chans)
}

type vrig struct {
	t      *testing.T
	bus    *bus.Bus
	plat   *pfake.Platform
	wake   *sfake.WakeWord
	stt    *sfake.STT
	tts    *sfake.TTS
	ai     *lfake.Provider
	p      *core.Pipeline
	broker *permissions.Broker
	timers *timers
	tool   *levelTool
	states *bus.Subscription[core.StateChange]
	tools  *bus.Subscription[core.ToolRan]
}

func mustv[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// newVRig builds the rig with a tool of level, and the permissions config
// toml (policies).
func newVRig(t *testing.T, level plugins.Permission, toml string) *vrig {
	t.Helper()
	cfg, err := config.Parse([]byte(toml))
	if err != nil {
		t.Fatal(err)
	}
	r := &vrig{t: t, timers: &timers{}, tool: &levelTool{name: "send_it", level: level}}
	r.bus = bus.New()
	r.plat = mustv(pfake.New(registry.Map{"root": t.TempDir()}))
	r.wake = mustv(sfake.NewWakeWord(registry.Map(nil)))
	r.stt = mustv(sfake.NewSTT(registry.Map(nil)))
	r.tts = mustv(sfake.NewTTS(registry.Map(nil)))
	r.ai = mustv(lfake.New(registry.Map(nil)))
	r.broker = &permissions.Broker{Bus: r.bus, Notifier: r.plat.Notifier(), Timeout: time.Minute}
	r.broker.Prompt = func(ctx context.Context, id string, q permissions.Request) func() {
		return core.PromptConfirm(ctx, core.ConfirmPrompt{ID: id, Question: q.Question, ClickOnly: q.ClickOnly, Level: q.Level.String()})
	}
	gate := &permissions.Gate{Config: func() *config.Config { return cfg }, Confirmer: r.broker}
	p, err := core.New(core.Deps{
		Bus: r.bus, Platform: r.plat, VAD: mustv(sfake.NewVAD(registry.Map(nil))), Wake: r.wake, STT: r.stt, TTS: r.tts,
		LLM: r.ai, Tools: []plugins.Tool{r.tool}, Gate: gate,
	}, core.Config{FollowUp: true, After: r.timers.After, Ack: core.FixedAck(audio.Clip{Format: audio.Speech, Samples: []float32{0.1, 0.1}})})
	if err != nil {
		t.Fatal(err)
	}
	r.p = p
	p.SetConfirmAnswerer(func(id string, allow bool) error { return r.broker.Answer(id, allow, permissions.HowVoice) })
	r.states = bus.Subscribe(r.bus, core.StateChanged, 256)
	r.tools = bus.Subscribe(r.bus, core.ToolCalled, 16)
	r.audio().Live()
	t.Cleanup(func() {
		p.Close()
		r.broker.Close()
		r.bus.Close()
	})
	return r
}

func (r *vrig) audio() *pfake.Audio       { return r.plat.Audio().(*pfake.Audio) }
func (r *vrig) notes() *pfake.Notifier    { return r.plat.Notifier().(*pfake.Notifier) }
func (r *vrig) launcher() *pfake.Launcher { return r.plat.Launcher().(*pfake.Launcher) }

func vawait[T any](t *testing.T, sub *bus.Subscription[T], ok func(T) bool) T {
	t.Helper()
	timeout := time.NewTimer(guardTime)
	defer timeout.Stop()
	for {
		select {
		case ev := <-sub.C():
			if ok(ev.Data) {
				return ev.Data
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for an event")
		}
	}
}

func frames(at time.Duration, n int) []audio.Frame {
	const size = 320
	var out []audio.Frame
	for i := range n + 3 {
		level := float32(0.5)
		if i >= n {
			level = 0
		}
		s := make([]float32, size)
		for j := range s {
			s[j] = level
		}
		out = append(out, audio.Frame{Format: audio.Speech, Samples: s, Offset: at + audio.Speech.Duration(i*size)})
	}
	return out
}

// ask has the AI call the tool (with args) in answer to a spoken command,
// and runs the pipeline.
func (r *vrig) ask(args string) {
	r.t.Helper()
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: r.tool.name, Args: json.RawMessage(args)}}},
		lfake.Reply{Text: "All done."},
	)
	r.askRaw()
}

// askRaw runs the pipeline for the command, with the AI's replies queued.
func (r *vrig) askRaw() {
	r.wake.Schedule(0) // the wake word
	r.audio().QueueCapture(frames(0, 3)...)
	r.stt.Queue(speech.Transcript{Text: "send it", Language: "en", Confidence: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 2*guardTime)
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	r.t.Cleanup(func() {
		cancel()
		<-done
	})
}

// say is the owner speaking, at stream offset at, and waits for it to be read.
func (r *vrig) say(text string, at time.Duration) {
	r.t.Helper()
	r.stt.Queue(speech.Transcript{Text: text, Language: "en", Confidence: 1})
	r.audio().QueueCapture(frames(at, 3)...)
	deadline := time.Now().Add(guardTime)
	for r.audio().Undelivered() > 0 {
		if time.Now().After(deadline) {
			r.t.Fatal("the microphone was not read")
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *vrig) awaitState(s core.State) {
	r.t.Helper()
	vawait(r.t, r.states, func(c core.StateChange) bool { return c.To == s })
}

func (r *vrig) spokenText() []string {
	var out []string
	for _, u := range r.tts.Spoken() {
		out = append(out, u.Text)
	}
	return out
}

// confirmation returns the tool call's outcome once it has been decided.
func (r *vrig) outcome() core.ToolRan {
	return vawait(r.t, r.tools, func(e core.ToolRan) bool { return e.Name == r.tool.name })
}

// clickAllow clicks Allow on the confirmation's notification.
func (r *vrig) click(action string) {
	r.t.Helper()
	deadline := time.Now().Add(guardTime)
	for {
		for i, n := range r.notes().Shown() {
			if len(n.Actions) > 0 && r.notes().Click(i, action) {
				return
			}
		}
		if time.Now().After(deadline) {
			r.t.Fatal("no confirmation notification")
		}
		time.Sleep(time.Millisecond)
	}
}

// A spoken yes approves an Act action (here the AI's open_url, which the
// config asks to confirm) and an Outward one; the decision is recorded as
// "voice".
func TestVoiceYesApproves(t *testing.T) {
	for _, level := range []plugins.Permission{plugins.Act, plugins.Outward} {
		for _, phrase := range []string{"yes", "Yes.", "yes please", "Yes, please.", "do it", "go ahead", "confirm"} {
			t.Run(level.String()+"/"+phrase, func(t *testing.T) {
				r := newVRig(t, level, "version = 1\n[permissions]\nact = \"confirm\"\n")
				r.ask(`{"url":"https://www.example.com/page"}`)
				r.awaitState(core.FollowUp)
				// The question is asked aloud, naming the site.
				if got := r.spokenText(); len(got) != 1 || got[0] != "Shall I open example.com?" {
					t.Fatalf("spoken %q", got)
				}
				r.say(phrase, 2*time.Second)
				ev := r.outcome()
				if ev.IsError || ev.Confirmation != permissions.HowVoice {
					t.Fatalf("ToolRan = %+v, want allowed by voice", ev)
				}
				if r.tool.runs() != 1 {
					t.Errorf("the tool ran %d times, want 1", r.tool.runs())
				}
			})
		}
	}
}

// "no", "cancel", "don't" and "never mind" refuse it, by voice.
func TestVoiceNoDenies(t *testing.T) {
	for _, phrase := range []string{"no", "No.", "cancel", "don't", "Don’t.", "never mind"} {
		t.Run(phrase, func(t *testing.T) {
			r := newVRig(t, plugins.Outward, "")
			r.ask(`{"to":"x@example.com"}`)
			r.awaitState(core.FollowUp)
			r.say(phrase, 2*time.Second)
			ev := r.outcome()
			if !ev.IsError || ev.Confirmation != permissions.HowVoice || !strings.Contains(ev.Result, "refused") {
				t.Fatalf("ToolRan = %+v, want refused by voice", ev)
			}
			if r.tool.runs() != 0 {
				t.Error("the tool ran after a no")
			}
		})
	}
}

// The whole utterance has to be an answer: "yes, send it to everyone" is not
// a yes, and neither is anything else. The notification still works.
func TestVoiceAnswerMustBeTheWholeUtterance(t *testing.T) {
	r := newVRig(t, plugins.Outward, "")
	r.ask(`{"to":"x@example.com"}`)
	r.awaitState(core.FollowUp)
	for i, said := range []string{"yes, send it to everyone", "maybe", "yes please do it now"} {
		r.say(said, time.Duration(2+3*i)*time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	if r.tool.runs() != 0 {
		t.Fatal("something that was not a yes approved it")
	}
	r.click(permissions.ActionAllow)
	if ev := r.outcome(); ev.IsError || ev.Confirmation != permissions.HowClick {
		t.Errorf("ToolRan = %+v, want allowed by click", ev)
	}
}

// Voice never approves a Destructive action: Footman says to confirm on
// screen, listens for nothing, and only the click works.
func TestVoiceNeverApprovesDestructive(t *testing.T) {
	r := newVRig(t, plugins.Destructive, "")
	r.ask(`{"path":"/home/me/report.txt"}`)
	// It is announced, and no window opens for an answer.
	deadline := time.Now().Add(guardTime)
	for len(r.spokenText()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("nothing was said")
		}
		time.Sleep(time.Millisecond)
	}
	if got := strings.Join(r.spokenText(), " "); got != "This is a destructive action. Please confirm on screen." {
		t.Errorf("spoken %q", got)
	}
	r.say("yes", 2*time.Second)
	r.say("go ahead", 5*time.Second)
	time.Sleep(20 * time.Millisecond)
	if r.timers.count() != 0 {
		t.Error("a follow-up window opened for a click-only confirmation")
	}
	if r.tool.runs() != 0 {
		t.Fatal("a spoken yes approved a destructive action")
	}
	if pending := r.broker.Pending(); len(pending) != 1 || !pending[0].ClickOnly {
		t.Fatalf("pending = %+v, want the one click-only request", pending)
	}
	r.click(permissions.ActionAllow)
	if ev := r.outcome(); ev.IsError || ev.Confirmation != permissions.HowClick {
		t.Errorf("ToolRan = %+v, want allowed by click", ev)
	}
}

// With the window open the notification button works too, and the first
// answer wins.
func TestClickWorksWhileListening(t *testing.T) {
	for _, action := range []string{permissions.ActionAllow, permissions.ActionDeny} {
		t.Run(action, func(t *testing.T) {
			r := newVRig(t, plugins.Outward, "")
			r.ask(`{"to":"x@example.com"}`)
			r.awaitState(core.FollowUp)
			r.click(action)
			ev := r.outcome()
			if ev.Confirmation != permissions.HowClick || ev.IsError != (action == permissions.ActionDeny) {
				t.Errorf("ToolRan = %+v after %s", ev, action)
			}
			// A late "yes" changes nothing.
			r.say("yes", 2*time.Second)
			time.Sleep(20 * time.Millisecond)
			wantRuns := 0
			if action == permissions.ActionAllow {
				wantRuns = 1
			}
			if r.tool.runs() != wantRuns {
				t.Errorf("the tool ran %d times, want %d", r.tool.runs(), wantRuns)
			}
		})
	}
}

// When the window has timed out the confirmation is still pending; a yes
// after the wake word answers it.
func TestVoiceYesAfterTheWindowWithTheWakeWord(t *testing.T) {
	r := newVRig(t, plugins.Outward, "")
	r.wake.Schedule(6) // the first frame of the speech after the command's six frames
	r.ask(`{"to":"x@example.com"}`)
	r.awaitState(core.FollowUp)
	r.timers.fireLast()
	r.awaitState(core.RunningTool)
	r.say("footman, yes", 2*time.Second)
	if ev := r.outcome(); ev.IsError || ev.Confirmation != permissions.HowVoice {
		t.Errorf("ToolRan = %+v, want allowed by voice", ev)
	}
}

// A confirmation asked for a call made by text (nothing spoken) is only
// shown: no question aloud, no window.
func TestNoSpokenQuestionForTypedRequests(t *testing.T) {
	r := newVRig(t, plugins.Outward, "")
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "t1", Name: r.tool.name, Args: json.RawMessage(`{}`)}}},
		lfake.Reply{Text: "All done."},
	)
	done := make(chan error, 1)
	go func() {
		done <- r.p.Submit(context.Background(), core.Utterance{Trigger: core.TriggerCommand, Text: "send it", Typed: true})
	}()
	r.click(permissions.ActionAllow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.tts.Spoken()) != 0 || r.timers.count() != 0 {
		t.Errorf("spoke %q and opened %d windows for a typed request", r.spokenText(), r.timers.count())
	}
	if ev := r.outcome(); ev.Confirmation != permissions.HowClick || r.tool.runs() != 1 {
		t.Errorf("ToolRan = %+v, ran %d", ev, r.tool.runs())
	}
}

// The break-glass stop ends the confirmation and its window; the call is
// refused and nothing runs.
func TestBreakGlassEndsAVoiceConfirmation(t *testing.T) {
	r := newVRig(t, plugins.Outward, "")
	r.ask(`{"to":"x@example.com"}`)
	r.awaitState(core.FollowUp)
	r.p.Stop()
	r.awaitState(core.Idle)
	if r.tool.runs() != 0 {
		t.Error("the tool ran after a stop")
	}
	deadline := time.Now().Add(guardTime)
	for len(r.broker.Pending()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the confirmation is still pending after a stop")
		}
		time.Sleep(time.Millisecond)
	}
}
