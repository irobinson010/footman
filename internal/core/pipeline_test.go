package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/jobs"
	jfake "github.com/irobinson010/footman/internal/jobs/fake"
	"github.com/irobinson010/footman/internal/llm"
	lfake "github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/plugins"
	tfake "github.com/irobinson010/footman/internal/plugins/fake"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	sfake "github.com/irobinson010/footman/internal/speech/fake"
)

// guard bounds every wait so a broken pipeline fails the test instead of
// hanging it. Nothing sleeps: waits end as soon as the awaited event arrives.
const guard = 10 * time.Second

func clock() time.Time { return time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC) }

// rig is a pipeline wired to fakes only.
type rig struct {
	t      *testing.T
	bus    *bus.Bus
	plat   *pfake.Platform
	wake   *sfake.WakeWord
	stt    *sfake.STT
	tts    *sfake.TTS
	ai     *lfake.Provider
	tool   *tfake.Tool
	worker *jfake.Worker
	p      *core.Pipeline
	states *bus.Subscription[core.StateChange]
}

// must panics on a setup error from a fake's constructor.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func newRig(t *testing.T, edit func(*core.Deps, *core.Config)) *rig {
	t.Helper()
	r := &rig{t: t, bus: bus.New(bus.WithClock(clock))}
	r.plat = must(pfake.New(registry.Map{"root": t.TempDir()}))
	r.wake = must(sfake.NewWakeWord(registry.Map(nil)))
	r.stt = must(sfake.NewSTT(registry.Map(nil)))
	r.tts = must(sfake.NewTTS(registry.Map(nil)))
	r.ai = must(lfake.New(registry.Map(nil)))
	r.tool = must(tfake.New(registry.Map{"name": "weather", "description": "Current weather."}))
	r.worker = must(jfake.New(registry.Map(nil)))
	d := core.Deps{
		Bus: r.bus, Platform: r.plat,
		VAD: must(sfake.NewVAD(registry.Map(nil))), Wake: r.wake, STT: r.stt, TTS: r.tts,
		LLM: r.ai, Tools: []plugins.Tool{r.tool}, Worker: r.worker,
	}
	cfg := core.Config{
		DictateHotkey: "F8", CommandHotkey: "Shift+F8", StopHotkey: "Ctrl+F8",
		Ack: core.FixedAck(audio.Clip{Format: audio.Speech, Samples: []float32{0.1, 0.1}}),
	}
	if edit != nil {
		edit(&d, &cfg)
	}
	r.p = must(core.New(d, cfg))
	r.states = bus.Subscribe(r.bus, core.StateChanged, 256)
	t.Cleanup(func() {
		r.p.Close()
		r.bus.Close()
	})
	return r
}

// say queues speech for the fake microphone: n loud frames then one silent
// frame, which ends the VAD segment.
func (r *rig) say(n int) {
	const size = 320
	var frames []audio.Frame
	for i := range n + 1 {
		level := float32(0.5)
		if i == n {
			level = 0
		}
		s := make([]float32, size)
		for j := range s {
			s[j] = level
		}
		frames = append(frames, audio.Frame{Format: audio.Speech, Samples: s, Offset: audio.Speech.Duration(i * size)})
	}
	r.plat.Audio().(*pfake.Audio).QueueCapture(frames...)
}

func (r *rig) transcript(text string) {
	r.stt.Queue(speech.Transcript{Text: text, Language: "en", Confidence: 1})
}

func (r *rig) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	r.t.Cleanup(cancel)
	return ctx
}

func (r *rig) submit(trig core.Trigger, text string) {
	r.t.Helper()
	if err := r.p.Submit(r.ctx(), core.Utterance{Trigger: trig, Text: text}); err != nil {
		r.t.Fatalf("Submit(%v, %q): %v", trig, text, err)
	}
}

// path returns the states entered so far, draining the subscription.
func (r *rig) path() []core.State {
	var out []core.State
	for {
		select {
		case ev := <-r.states.C():
			out = append(out, ev.Data.To)
		default:
			return out
		}
	}
}

// pathTo is path for a turn that ends asynchronously: it waits (up to 2 s)
// until last has been entered, then returns the states so far.
func (r *rig) pathTo(last core.State) []core.State {
	r.t.Helper()
	var out []core.State
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-r.states.C():
			out = append(out, ev.Data.To)
			if ev.Data.To == last {
				return append(out, r.path()...)
			}
		case <-timeout:
			return out
		}
	}
}

func (r *rig) input() *pfake.Input       { return r.plat.Input().(*pfake.Input) }
func (r *rig) launcher() *pfake.Launcher { return r.plat.Launcher().(*pfake.Launcher) }
func (r *rig) speaker() *pfake.Audio     { return r.plat.Audio().(*pfake.Audio) }

// await reads sub until an event matches ok.
func await[T any](t *testing.T, sub *bus.Subscription[T], ok func(T) bool) T {
	t.Helper()
	timeout := time.NewTimer(guard)
	defer timeout.Stop()
	for {
		select {
		case ev, open := <-sub.C():
			if !open {
				t.Fatal("subscription closed")
			}
			if ok(ev.Data) {
				return ev.Data
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for an event")
		}
	}
}

func equal[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func spoken(tts *sfake.TTS) []string {
	var out []string
	for _, u := range tts.Spoken() {
		out = append(out, u.Text)
	}
	return out
}

// Fake audio → fake wake word and VAD → fake STT → router → cleanup → the
// fake platform receives typed text.
func TestDictationEndToEnd(t *testing.T) {
	r := newRig(t, nil)
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("Footman, dictate um, hello hello world")

	if err := r.p.Run(r.ctx()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	equal(t, "typed", r.input().Typed(), []string{"hello world"})
	equal(t, "states", r.path(), []core.State{
		core.Listening, core.Transcribing, core.Routing, core.Cleaning, core.Inserting, core.Idle,
	})
	if n := len(r.stt.Inputs()); n != 1 {
		t.Errorf("STT called %d times, want 1", n)
	}
	if got := r.p.State(); got != core.Idle {
		t.Errorf("final state %v, want idle", got)
	}
}

// layoutInput is a fake Input that knows the keyboard layout
// (dictation.Layout), like the Linux one.
type layoutInput struct {
	*pfake.Input
	typeable func(string) bool
}

func (l layoutInput) Typeable(s string) bool { return l.typeable(s) }

type layoutPlatform struct {
	*pfake.Platform
	in layoutInput
}

func (p layoutPlatform) Input() platform.Input { return p.in }

// Auto insertion asks the platform's Input which text the layout can type,
// instead of assuming ASCII.
func TestAutoInsertUsesPlatformLayout(t *testing.T) {
	var asked []string
	var lp layoutPlatform
	r := newRig(t, func(d *core.Deps, _ *core.Config) {
		lp = layoutPlatform{Platform: d.Platform.(*pfake.Platform)}
		lp.in = layoutInput{Input: lp.Platform.Input().(*pfake.Input), typeable: func(s string) bool {
			asked = append(asked, s)
			return true // say, a French layout: é is on it
		}}
		d.Platform = lp
	})
	r.p.Arm(core.TriggerDictate)
	r.say(2)
	r.transcript("café au lait")
	if err := r.p.Run(r.ctx()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	equal(t, "typed", lp.in.Typed(), []string{"café au lait"})
	equal(t, "asked", asked, []string{"café au lait"})
}

// The dictation hotkey always means dictation; text with a line break is
// pasted (ADR-0022).
func TestDictationHotkeyPastesMultiline(t *testing.T) {
	r := newRig(t, nil)
	r.p.Arm(core.TriggerDictate)
	r.say(2)
	r.transcript("Open the pod bay doors. New line. Sorry, Dave, scratch that. Um, I can't do that")

	if err := r.p.Run(r.ctx()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	equal(t, "pasted", r.input().Pasted(), []string{"Open the pod bay doors.\nI can't do that"})
	equal(t, "typed", r.input().Typed(), nil)
	if len(r.launcher().Opened()) != 0 {
		t.Error("dictation was treated as a command")
	}
}

// Hotkeys arrive through platform.Input: the stop hotkey stops, and Unmute
// reopens the wake-word microphone, where an armed utterance is handled.
// (The push-to-talk keys are covered in ptt_test.go.)
func TestHotkeys(t *testing.T) {
	r := newRig(t, nil)
	r.p.Stop() // start muted so Run waits on the hotkeys
	mic := bus.Subscribe(r.bus, core.MicChanged, 16)
	stops := bus.Subscribe(r.bus, core.StopDone, 16)
	r.say(2)
	r.transcript("hello there")

	done := make(chan error, 1)
	go func() { done <- r.p.Run(r.ctx()) }()
	await(t, mic, func(s platform.TrayStatus) bool { return s == platform.MicOff }) // hotkeys are registered

	if n := r.input().Press("Ctrl+F8", true); n != 1 {
		t.Fatalf("stop hotkey reached %d listeners", n)
	}
	if got := await(t, stops, func(core.Stopped) bool { return true }); got.Source != core.StopHotkey {
		t.Errorf("stop source %q, want hotkey", got.Source)
	}
	r.path()
	r.p.Arm(core.TriggerDictate)
	await(t, r.states, func(c core.StateChange) bool { return c.To == core.Listening })
	r.p.Unmute()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	equal(t, "typed", r.input().Typed(), []string{"hello there"})
}

// A command goes to the AI, which calls a tool; the tool's result goes back
// and the reply is spoken after the acknowledgement clip.
func TestCommandWithToolCall(t *testing.T) {
	r := newRig(t, nil)
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "weather", Args: json.RawMessage(`{"city":"Leeds"}`)}}},
		lfake.Reply{Text: "Sunny and 20 degrees."},
	)
	r.tool.Queue(plugins.Result{Text: "sunny, 20C"})
	tools := bus.Subscribe(r.bus, core.ToolCalled, 8)

	r.submit(core.TriggerWake, "Footman, what's the weather in Leeds?")

	if calls := r.tool.Calls(); len(calls) != 1 || string(calls[0]) != `{"city":"Leeds"}` {
		t.Errorf("tool calls = %s", calls)
	}
	reqs := r.ai.Requests()
	if len(reqs) != 2 {
		t.Fatalf("AI called %d times, want 2", len(reqs))
	}
	if got := reqs[0].Messages[0].Text; got != "what's the weather in Leeds?" {
		t.Errorf("AI was asked %q", got)
	}
	names := []string{}
	for _, d := range reqs[0].Tools {
		names = append(names, d.Name)
	}
	equal(t, "tools offered", names, []string{"weather", "open_url", "open_app", "quick_action", core.SystemStatusTool, core.StartTaskTool})
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != llm.ToolResult || last.ToolCallID != "c1" || last.Text != "sunny, 20C" {
		t.Errorf("tool result message = %+v", last)
	}
	if ev := await(t, tools, func(core.ToolRan) bool { return true }); ev.Name != "weather" || ev.IsError {
		t.Errorf("ToolRan = %+v", ev)
	}
	equal(t, "spoken", spoken(r.tts), []string{"Sunny and 20 degrees."})
	if played := r.speaker().Played(); len(played) != 2 || len(played[0].Samples) != 2 {
		t.Errorf("played %d clips, want the acknowledgement then the reply", len(played))
	}
	equal(t, "states", r.path(), []core.State{
		core.Routing, core.Acknowledging, core.Thinking, core.RunningTool, core.Thinking, core.Replying, core.Idle,
	})
}

// Outward and destructive tools are refused until confirmations exist; the
// AI is told so and the tool never runs.
func TestGateRefusesOutwardTool(t *testing.T) {
	var mail *tfake.Tool
	r := newRig(t, func(d *core.Deps, _ *core.Config) {
		mail = must(tfake.New(registry.Map{"name": "send_email", "permission": "outward"}))
		d.Tools = append(d.Tools, mail)
	})
	r.ai.Queue(
		lfake.Reply{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "send_email", Args: json.RawMessage(`{}`)}}},
		lfake.Reply{Text: "I need your confirmation for that."},
	)
	r.submit(core.TriggerCommand, "email Bob the report")
	if len(mail.Calls()) != 0 {
		t.Error("outward tool ran without confirmation")
	}
	msgs := r.ai.Requests()[1].Messages
	if res := msgs[len(msgs)-1]; !res.IsError {
		t.Errorf("tool result = %+v, want an error", res)
	}
}

func TestFastPath(t *testing.T) {
	cases := []struct {
		say  string
		want pfake.Opened
	}{
		{"Footman, open YouTube.", pfake.Opened{Kind: "url", Target: "https://www.youtube.com"}},
		{"footman open example.com/docs", pfake.Opened{Kind: "url", Target: "https://example.com/docs"}},
		{"Footman, open https://go.dev", pfake.Opened{Kind: "url", Target: "https://go.dev"}},
		{"Footman, launch Firefox", pfake.Opened{Kind: "app", Target: "Firefox"}},
	}
	for _, c := range cases {
		t.Run(c.say, func(t *testing.T) {
			r := newRig(t, nil)
			r.submit(core.TriggerWake, c.say)
			if got := r.launcher().Opened(); len(got) != 1 || got[0] != c.want {
				t.Errorf("opened %v, want %v", got, c.want)
			}
			if len(r.ai.Requests()) != 0 {
				t.Error("fast path command went to the AI")
			}
			equal(t, "states", r.path(), []core.State{core.Routing, core.Acknowledging, core.FastPath, core.Replying, core.Idle})
		})
	}
}

func TestFastPathVolumeMedia(t *testing.T) {
	cases := []struct {
		say   string
		want  string
		reply string
	}{
		{"Footman, volume down", "volume -10", "Volume down."},
		{"footman turn the volume up", "volume +10", "Volume up."},
		{"footman mute", "mute", "Muted."},
		{"footman unmute", "unmute", "Unmuted."},
		{"Footman, set volume to 40%", "volume =40", "Volume 40 percent."},
		{"footman volume 25 percent", "volume =25", "Volume 25 percent."},
		{"Footman, pause music", "media pause", "Paused."},
		{"footman play", "media play", "Playing."},
		{"footman next track", "media next", "Next."},
		{"footman previous song", "media previous", "Previous."},
		{"Footman, and volume down.", "volume -10", "Volume down."},
		{"footman okay, pause the music", "media pause", "Paused."},
	}
	for _, c := range cases {
		t.Run(c.say, func(t *testing.T) {
			r := newRig(t, nil)
			r.submit(core.TriggerWake, c.say)
			equal(t, "controls", r.plat.Controls(), []string{c.want})
			equal(t, "spoken", spoken(r.tts), []string{c.reply})
			if len(r.ai.Requests()) != 0 {
				t.Error("fast path command went to the AI")
			}
		})
	}
}

func TestFastRulesDeclareAct(t *testing.T) {
	for _, rule := range core.DefaultFastRules(map[string]string{"Docs": "https://go.dev"}) {
		if rule.Permission != plugins.Act {
			t.Errorf("rule %s permission = %v, want act", rule.Name, rule.Permission)
		}
	}
	rule := core.DefaultFastRules(map[string]string{"Docs": "https://go.dev"})[0]
	if _, ok := rule.Match("open docs"); !ok {
		t.Error("user alias not matched")
	}
	for _, text := range []string{"volume", "pause the meeting now", "set the table", "volume 150", "next week"} {
		for _, rule := range core.DefaultFastRules(nil)[1:] {
			if _, ok := rule.Match(text); ok {
				t.Errorf("rule %s matched %q", rule.Name, text)
			}
		}
	}
}

// A long task becomes a background job, and the pipeline is idle as soon as
// the handover is acknowledged, while the job is still running.
func TestLongTaskBecomesJob(t *testing.T) {
	r := newRig(t, nil)
	r.worker.Queue(jfake.Outcome{Hold: true, Result: jobs.Result{Summary: "Parser refactored."}})
	r.ai.Queue(lfake.Reply{ToolCalls: []llm.ToolCall{{
		ID: "c1", Name: core.StartTaskTool, Args: json.RawMessage(`{"prompt":"refactor the parser"}`),
	}}})
	updates := bus.Subscribe(r.bus, core.JobUpdated, 16)

	r.submit(core.TriggerWake, "Footman, refactor the parser")

	if got := r.p.State(); got != core.Idle {
		t.Fatalf("state after handover = %v, want idle", got)
	}
	equal(t, "jobs", r.p.Jobs(), []string{"job-1"})
	j := r.worker.Jobs()[0]
	if j.Status() != jobs.Running || j.Task().Prompt != "refactor the parser" {
		t.Fatalf("job = %v %+v", j.Status(), j.Task())
	}
	equal(t, "spoken", spoken(r.tts), []string{"On it.", "I'll let you know when it's done."})
	if n := len(r.ai.Requests()); n != 1 {
		t.Errorf("AI called %d times, want 1", n)
	}

	j.Release()
	ev := await(t, updates, func(e core.JobEvent) bool { return e.Update.Status.Final() })
	if ev.ID != "job-1" || ev.Update.Status != jobs.Succeeded || ev.Update.Message != "Parser refactored." {
		t.Errorf("final update = %+v", ev)
	}
}

// startHeldJob starts a job that runs until cancelled.
func (r *rig) startHeldJob() *jfake.Job {
	r.t.Helper()
	r.worker.Queue(jfake.Outcome{Hold: true})
	if _, err := r.p.StartJob(jobs.Task{Prompt: "long thing"}); err != nil {
		r.t.Fatal(err)
	}
	return r.worker.Jobs()[len(r.worker.Jobs())-1]
}

func waitCancelled(t *testing.T, j *jfake.Job) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	if _, err := j.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("job Wait = %v, want context.Canceled", err)
	}
	if j.Status() != jobs.Cancelled {
		t.Errorf("job status %v, want cancelled", j.Status())
	}
}

// "<wake word>, stop everything" from the microphone cancels running jobs and
// mutes the mic.
func TestBreakGlassCancelsJob(t *testing.T) {
	r := newRig(t, nil)
	j := r.startHeldJob()
	stops := bus.Subscribe(r.bus, core.StopDone, 4)
	r.wake.Schedule(0)
	r.say(2)
	r.transcript("Footman, stop everything!")

	// After a stop, Run stays up for the hotkeys with the mic muted, so end
	// it once the stop has happened.
	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	ev := await(t, stops, func(core.Stopped) bool { return true })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	waitCancelled(t, j)
	if ev.Source != core.StopVoice || !slices.Equal(ev.Jobs, []string{"job-1"}) {
		t.Errorf("Stopped = %+v", ev)
	}
	if !r.p.Muted() {
		t.Error("microphone not muted")
	}
	if tray := r.plat.Notifier().(*pfake.Notifier).Tray(); len(tray) == 0 || tray[len(tray)-1] != platform.MicOff {
		t.Errorf("tray = %v, want MicOff last", tray)
	}
	if len(r.ai.Requests()) != 0 || len(r.input().Typed()) != 0 {
		t.Error("stop phrase was routed")
	}
}

// A break-glass event from the wake-word spotter stops at once, without a
// transcript.
func TestBreakGlassEventStops(t *testing.T) {
	r := newRig(t, nil)
	j := r.startHeldJob()
	stops := bus.Subscribe(r.bus, core.StopDone, 4)
	r.wake.Kind, r.wake.Keyword = speech.WakeKindBreakGlass, "stop everything"
	r.wake.Schedule(0)
	r.say(2)

	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	ev := await(t, stops, func(core.Stopped) bool { return true })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	waitCancelled(t, j)
	if ev.Source != core.StopVoice || !r.p.Muted() {
		t.Errorf("Stopped = %+v, muted %v", ev, r.p.Muted())
	}
	if n := len(r.stt.Inputs()); n != 0 {
		t.Errorf("STT called %d times", n)
	}
}

// Stop (the hotkey and tray path) also cancels jobs.
func TestStopCancelsJob(t *testing.T) {
	r := newRig(t, nil)
	j := r.startHeldJob()
	r.p.Stop()
	waitCancelled(t, j)
	if !r.p.Muted() {
		t.Error("microphone not muted")
	}
	r.p.Unmute()
	if r.p.Muted() {
		t.Error("still muted after Unmute")
	}
}

// "Stop everything" without the wake word does nothing (ADR-0017): unaddressed
// speech is never transcribed, dictation types it, and a hotkey command is an
// ordinary command.
func TestStopWithoutWakeWordDoesNothing(t *testing.T) {
	r := newRig(t, nil)
	j := r.startHeldJob()
	stops := bus.Subscribe(r.bus, core.StopDone, 4)

	// Said aloud with no wake word or hotkey: ignored entirely.
	r.say(2)
	r.transcript("stop everything")
	if err := r.p.Run(r.ctx()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := len(r.stt.Inputs()); n != 0 {
		t.Errorf("unaddressed speech was transcribed %d times", n)
	}

	r.submit(core.TriggerDictate, "stop everything")
	equal(t, "typed", r.input().Typed(), []string{"stop everything"})

	r.ai.Queue(lfake.Reply{Text: "Stop what?"})
	r.submit(core.TriggerCommand, "stop everything")
	equal(t, "spoken", spoken(r.tts), []string{"Stop what?"})

	if j.Status() != jobs.Running {
		t.Errorf("job status %v, want running", j.Status())
	}
	if r.p.Muted() {
		t.Error("microphone muted")
	}
	select {
	case ev := <-stops.C():
		t.Errorf("unexpected stop %+v", ev.Data)
	default:
	}
}

// blockingTTS speaks forever, until its context is cancelled.
type blockingTTS struct{ started chan struct{} }

func (b blockingTTS) Speak(ctx context.Context, _ string, _ speech.Voice) (speech.AudioStream, error) {
	close(b.started)
	return blockingStream{ctx}, nil
}

type blockingStream struct{ ctx context.Context }

func (blockingStream) Format() audio.Format { return audio.Speech }
func (s blockingStream) Recv() (audio.Frame, error) {
	<-s.ctx.Done()
	return audio.Frame{}, s.ctx.Err()
}
func (blockingStream) Close() error { return nil }

// While Footman is talking, other utterances are refused as busy, but the
// break-glass phrase still gets through and cuts the speech off.
func TestBreakGlassStopsSpeechWhileBusy(t *testing.T) {
	started := make(chan struct{})
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.TTS = blockingTTS{started} })
	r.ai.Queue(lfake.Reply{Text: "Once upon a time..."})
	stops := bus.Subscribe(r.bus, core.StopDone, 4)

	done := make(chan error, 1)
	go func() {
		done <- r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerWake, Text: "footman, tell me a story"})
	}()
	<-started

	if err := r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerCommand, Text: "what time is it"}); !errors.Is(err, core.ErrBusy) {
		t.Errorf("second Submit = %v, want ErrBusy", err)
	}
	r.submit(core.TriggerWake, "Footman, cancel everything")

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("story Submit = %v, want context.Canceled", err)
	}
	if ev := await(t, stops, func(core.Stopped) bool { return true }); ev.Turns != 1 {
		t.Errorf("Stopped = %+v, want 1 turn cancelled", ev)
	}
	if got := r.p.State(); got != core.Idle {
		t.Errorf("state %v, want idle", got)
	}
}

// Without the wake word and a clear command, a wake is dropped silently.
func TestFalseWakeIsDropped(t *testing.T) {
	r := newRig(t, nil)
	r.submit(core.TriggerWake, "Footman.")
	if len(r.ai.Requests()) != 0 || len(r.tts.Spoken()) != 0 || len(r.input().Typed()) != 0 {
		t.Error("false wake did something")
	}
	equal(t, "states", r.path(), []core.State{core.Routing, core.Idle})
}

// Without TTS, replies are shown as notifications.
func TestReplyAsNotification(t *testing.T) {
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.TTS = nil })
	r.ai.Queue(lfake.Reply{Text: "Done."})
	r.submit(core.TriggerCommand, "tidy up")
	shown := r.plat.Notifier().(*pfake.Notifier).Shown()
	if len(shown) != 1 || shown[0].Body != "Done." {
		t.Errorf("notifications = %+v", shown)
	}
}

func TestErrorsArePublished(t *testing.T) {
	r := newRig(t, nil)
	fails := bus.Subscribe(r.bus, core.Failed, 4)
	r.ai.Queue(lfake.Reply{Err: errors.New("quota exceeded")})
	if err := r.p.Submit(r.ctx(), core.Utterance{Trigger: core.TriggerCommand, Text: "hi"}); err == nil {
		t.Fatal("Submit succeeded despite the AI failing")
	}
	if ev := await(t, fails, func(core.Failure) bool { return true }); ev.State != core.Thinking {
		t.Errorf("failure = %+v", ev)
	}
	if r.p.State() != core.Idle {
		t.Errorf("state %v after failure, want idle", r.p.State())
	}
}

func TestNewAndRunNeedDeps(t *testing.T) {
	if _, err := core.New(core.Deps{}, core.Config{}); err == nil {
		t.Error("New without deps succeeded")
	}
	r := newRig(t, func(d *core.Deps, _ *core.Config) { d.VAD = nil })
	if err := r.p.Run(r.ctx()); err == nil {
		t.Error("Run without a VAD succeeded")
	}
}

// The AI can change the volume and control playback through quick_action.
func TestQuickActionTool(t *testing.T) {
	r := newRig(t, nil)
	r.ai.Queue(lfake.Reply{ToolCalls: []llm.ToolCall{{
		ID: "c1", Name: "quick_action", Args: json.RawMessage(`{"action":"volume down"}`),
	}}}, lfake.Reply{Text: "Turned it down."})
	r.submit(core.TriggerWake, "footman it's a bit loud in here")
	equal(t, "controls", r.plat.Controls(), []string{"volume -10"})
}
