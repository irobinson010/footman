package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
)

// fakeFinder returns scripted matches and records the names asked for.
type fakeFinder struct {
	mu    sync.Mutex
	found []string
	asked []string
}

func (f *fakeFinder) Find(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, name)
	return f.found, nil
}

func (r *rig) windows() *pfake.Windows   { return r.plat.Windows().(*pfake.Windows) }
func (r *rig) notifier() *pfake.Notifier { return r.plat.Notifier().(*pfake.Notifier) }

// dictRig is a rig whose wake word is "alfred", with fast focus timings and a
// notes folder in a temporary directory.
func dictRig(t *testing.T, edit func(*core.Deps, *core.Config)) *drig {
	t.Helper()
	notes := filepath.Join(t.TempDir(), "notes")
	r := newRig(t, func(d *core.Deps, c *core.Config) {
		c.WakeWords = []string{"alfred"}
		c.Dictation = core.DictationConfig{NotesDir: notes, IdleTimeout: time.Minute}
		c.FocusTimeout, c.FocusSettle, c.FocusPoll = 200*time.Millisecond, -1, time.Millisecond
		c.Now = func() time.Time { return time.Date(2026, 11, 27, 14, 30, 12, 0, time.UTC) }
		c.StopCue = core.FixedAck(audio.Clip{Format: audio.Speech, Samples: []float32{0.2, 0.2, 0.2, 0.2}})
		if edit != nil {
			edit(d, c)
		}
	})
	r.wake.Keyword = "alfred"
	r.windows().Set([]platform.Window{{ID: "1", Title: "Browser"}, {ID: "2", Title: "Editor"}}, "1")
	return &drig{rig: r, notes: notes}
}

// drig is a rig with a notes folder.
type drig struct {
	*rig
	notes string
}

// session scripts a wake-word start phrase followed by utterances spoken with
// no wake word, and runs the pipeline until the microphone stream ends.
func (r *rig) session(start string, utts ...string) {
	r.t.Helper()
	r.wake.Schedule(0)
	r.say(3)
	r.transcript(start)
	for _, u := range utts {
		r.say(2)
		r.transcript(u)
	}
	r.run()
}

// focusOnOpen moves focus to the editor window once the launcher has opened
// something, as a real editor would take focus.
func (r *rig) focusOnOpen() {
	go func() {
		deadline := time.Now().Add(guard)
		for time.Now().Before(deadline) {
			if len(r.launcher().Opened()) > 0 {
				r.windows().Set([]platform.Window{{ID: "1", Title: "Browser"}, {ID: "2", Title: "Editor"}}, "2")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

func (r *rig) typed() []string { return append(r.input().Typed(), r.input().Pasted()...) }

// The start phrases begin a session that types into the focused app, with
// the persona's own wake word and nothing spoken (the microphone is open).
func TestDictationStartPhrases(t *testing.T) {
	for _, c := range []struct{ wake, text string }{
		{"alfred", "Alfred write"},
		{"alfred", "Alfred, dictate."},
		{"alfred", "Alfred take a note"},
		{"alfred", "Alfred, start dictation."},
		{"klaus", "Klaus write"},
		{"klaus", "klaus, take a note."},
	} {
		t.Run(c.text, func(t *testing.T) {
			r := dictRig(t, func(_ *core.Deps, cfg *core.Config) { cfg.WakeWords = []string{c.wake} })
			status := bus.Subscribe(r.bus, core.DictationChanged, 8)
			r.submit(core.TriggerWake, c.text)
			st := r.p.Dictation()
			if !st.Active || st.Phase != core.PhaseDictating || !strings.Contains(st.Message, "Dictating — say 'stop dictation'") {
				t.Errorf("Dictation() = %+v", st)
			}
			ev := await(t, status, func(s core.DictationStatus) bool { return s.Active })
			if ev.Message != st.Message {
				t.Errorf("status event %q, want %q", ev.Message, st.Message)
			}
			if n := r.notifier().Shown(); len(n) != 1 || n[0].Body != st.Message {
				t.Errorf("notifications %+v, want the status shown once", n)
			}
			if got := spoken(r.tts); len(got) != 0 {
				t.Errorf("spoke %v with the microphone open", got)
			}
			if len(r.typed()) != 0 || len(r.launcher().Opened()) != 0 {
				t.Error("something was typed or opened")
			}
			tray := r.notifier().Tray()
			if len(tray) == 0 || tray[len(tray)-1] != platform.Recording {
				t.Errorf("tray %v, want recording last", tray)
			}
		})
	}
	// "write" with words after it is still a one-shot dictation, and a start
	// phrase starts nothing a second time.
	r := dictRig(t, nil)
	r.submit(core.TriggerWake, "Alfred write hello there")
	equal(t, "typed", r.typed(), []string{"hello there"})
	if r.p.Dictation().Active {
		t.Error("a one-shot dictation started a session")
	}
	r.submit(core.TriggerWake, "Alfred write")
	r.submit(core.TriggerWake, "Alfred write")
	if got := spoken(r.tts); len(got) != 1 || !strings.Contains(got[0], "already") {
		t.Errorf("second start said %v", got)
	}
}

// Utterances heard after the start are typed with no wake word, including
// ones spoken before the acknowledgement finished, and the stop phrase ends
// the session with a cue.
func TestDictationSessionTypesUntilStopPhrase(t *testing.T) {
	r := dictRig(t, nil)
	status := bus.Subscribe(r.bus, core.DictationChanged, 8)
	r.session("Alfred write", "Dear Sam period", "And one more thing", "Stop dictation.", "never typed")
	equal(t, "typed", r.typed(), []string{"Dear Sam.", "And one more thing"})
	st := await(t, status, func(s core.DictationStatus) bool { return !s.Active })
	if st.Reason != core.EndStopPhrase || !strings.HasPrefix(st.Message, "Dictation stopped") {
		t.Errorf("end status %+v", st)
	}
	if r.p.Dictation().Active {
		t.Error("session still active")
	}
	time.Sleep(20 * time.Millisecond)
	var cues int
	for _, c := range r.speaker().Played() {
		if len(c.Samples) == 4 {
			cues++
		}
	}
	if cues != 1 {
		t.Errorf("%d stop cues played, want 1", cues)
	}
	if n := r.notifier().Shown(); len(n) < 2 || !strings.HasPrefix(n[len(n)-1].Body, "Dictation stopped") {
		t.Errorf("notifications %+v", n)
	}
	if last := r.notifier().Tray(); last[len(last)-1] == platform.Recording {
		t.Errorf("tray still recording: %v", last)
	}
}

// A wake word said during a session is not a command.
func TestDictationIgnoresWakeWordDuringSession(t *testing.T) {
	r := dictRig(t, nil)
	r.wake.Schedule(6) // inside the second utterance
	r.session("Alfred write", "Alfred open youtube", "stop dictation")
	equal(t, "typed", r.typed(), []string{"Alfred open youtube"})
	if len(r.launcher().URLs()) != 0 {
		t.Error("a command was run during dictation")
	}
}

// Stop phrases count only as the whole utterance (a trailing period is fine),
// or at the end of a longer one after a sentence break, which is typed.
func TestDictationStopPhrases(t *testing.T) {
	for _, c := range []struct {
		name, say string
		phrases   []string
		typed     []string // what is typed from this utterance
		stops     bool
	}{
		{name: "whole", say: "stop dictation", stops: true},
		{name: "period", say: "Stop dictation.", stops: true},
		{name: "end", say: "End dictation", stops: true},
		{name: "thats all", say: "That's all.", stops: true},
		{name: "curly apostrophe", say: "That\u2019s all.", stops: true},
		{name: "wake stop", say: "Alfred, stop.", stops: true},
		{name: "wake stop dictation", say: "Alfred stop dictation", stops: true},
		{name: "embedded", say: "please stop dictation errors in the log", typed: []string{"please stop dictation errors in the log"}},
		{name: "thats all mid", say: "I think that's all we need", typed: []string{"I think that's all we need"}},
		{name: "thats all no break", say: "I hope that's all", typed: []string{"I hope that's all"}},
		{name: "bare stop", say: "Stop.", typed: []string{"Stop."}},
		{name: "trailing", say: "and that's the end. stop dictation", typed: []string{"and that's the end."}, stops: true},
		{name: "trailing wake comma", say: "All done, Alfred stop", typed: []string{"All done"}, stops: true},
		{name: "trailing wake no break", say: "All done Alfred stop", typed: []string{"All done Alfred stop"}},
		{name: "trailing wake after break", say: "All done. Alfred stop.", typed: []string{"All done."}, stops: true},
		{name: "custom", say: "we are done here", phrases: []string{"we are done here"}, stops: true},
		{name: "custom replaces default", say: "stop dictation", phrases: []string{"we are done here"}, typed: []string{"stop dictation"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := dictRig(t, func(_ *core.Deps, cfg *core.Config) { cfg.Dictation.StopPhrases = c.phrases })
			r.session("Alfred write", c.say, "after")
			want := c.typed
			if !c.stops {
				want = append(want, "after")
			}
			equal(t, "typed", r.typed(), want)
		})
	}
}

// The idle timeout ends a session after silence.
func TestDictationIdleTimeout(t *testing.T) {
	r := dictRig(t, func(_ *core.Deps, c *core.Config) { c.Dictation.IdleTimeout = 40 * time.Millisecond })
	r.speaker().KeepOpen()
	status := bus.Subscribe(r.bus, core.DictationChanged, 8)
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("Alfred write")
	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = <-done // the error is the cancellation
	})
	await(t, status, func(s core.DictationStatus) bool { return s.Active })
	st := await(t, status, func(s core.DictationStatus) bool { return !s.Active })
	if st.Reason != core.EndIdle {
		t.Errorf("ended for %q, want idle", st.Reason)
	}
	if r.p.Dictation().Active {
		t.Error("still dictating after the timeout")
	}
}

// Speech keeps the session alive: the timer restarts with each utterance.
func TestDictationIdleTimerRestartsOnSpeech(t *testing.T) {
	r := dictRig(t, func(_ *core.Deps, c *core.Config) { c.Dictation.IdleTimeout = 2 * time.Second })
	r.session("Alfred write", "one", "two", "three")
	equal(t, "typed", r.typed(), []string{"one", "two", "three"})
}

// The break-glass stop ends the session and mutes, as everywhere. (Run stays
// up muted after a stop, so it runs in the background here.)
func TestDictationBreakGlass(t *testing.T) {
	r := dictRig(t, nil)
	status := bus.Subscribe(r.bus, core.DictationChanged, 8)
	r.wake.Schedule(0)
	r.say(3)
	r.transcript("Alfred write")
	for _, u := range []string{"first", "Alfred, stop everything", "never typed"} {
		r.say(2)
		r.transcript(u)
	}
	ctx, cancel := context.WithCancel(r.ctx())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = <-done // the error is the cancellation
	})
	if st := await(t, status, func(s core.DictationStatus) bool { return !s.Active }); st.Reason != core.EndStopped {
		t.Errorf("ended for %q, want stopped", st.Reason)
	}
	equal(t, "typed", r.typed(), []string{"first"})
	if !r.p.Muted() {
		t.Error("not muted after the break-glass stop")
	}
	// The break-glass phrase without the wake word is just text.
	r = dictRig(t, nil)
	r.session("Alfred write", "stop everything")
	equal(t, "typed", r.typed(), []string{"stop everything"})
}

func TestDictationNavigationAndEditing(t *testing.T) {
	for say, want := range map[string]string{
		"Go to the end.":      "Ctrl+End",
		"go to the start":     "Ctrl+Home",
		"End of line":         "End",
		"start of line.":      "Home",
		"select all":          "Ctrl+A",
		"Undo.":               "Ctrl+Z",
		"redo":                "Ctrl+Shift+Z",
		"Press enter.":        "Enter",
		"press tab":           "Tab",
		"Press escape":        "Escape",
		"Save.":               "Ctrl+S",
		"save the file":       "Ctrl+S",
		"go to the beginning": "Ctrl+Home",
	} {
		t.Run(say, func(t *testing.T) {
			r := dictRig(t, nil)
			r.session("Alfred write", say)
			equal(t, "keys", r.input().Keys(), []string{want})
			if len(r.typed()) != 0 {
				t.Errorf("typed %v for a command", r.typed())
			}
		})
	}
	// Only a whole utterance is a command.
	r := dictRig(t, nil)
	r.session("Alfred write", "please save the file for me", "I will go to the end of the road", "press enter to continue")
	equal(t, "typed", r.typed(), []string{"please save the file for me", "I will go to the end of the road", "press enter to continue"})
	equal(t, "keys", r.input().Keys(), nil)

	// "new line" and "scratch that" / "delete that" are the formatter's.
	r = dictRig(t, nil)
	r.session("Alfred write", "hello world", "delete that", "first new line second")
	equal(t, "typed", r.typed(), []string{"hello world", "first\nsecond"})
	equal(t, "backspaces", r.input().Backspaces(), []int{11})
}

// A navigation command cancels what "scratch that" may take back.
func TestDictationScratchAfterNavigation(t *testing.T) {
	r := dictRig(t, nil)
	r.session("Alfred write", "hello world", "go to the end", "scratch that")
	equal(t, "backspaces", r.input().Backspaces(), nil)
}

// In a terminal Enter is never pressed, not even by "press enter" (ADR-0032),
// and shortcuts a terminal reads as signals are not sent. Line movement, Tab
// and Escape are.
func TestDictationTerminalSafety(t *testing.T) {
	r := dictRig(t, nil)
	r.input().Terminal = true
	r.session("Alfred write", "ls space dash l", "press enter", "New line.", "undo", "save", "select all",
		"go to the end", "press tab", "end of line", "start of line", "press escape", "echo hi new paragraph")
	equal(t, "keys", r.input().Keys(), []string{"Tab", "End", "Home", "Escape"})
	equal(t, "typed", r.typed(), []string{"ls -l", "echo hi"})
	var blocked int
	for _, n := range r.notifier().Shown() {
		if strings.Contains(n.Body, "terminal has focus") {
			blocked++
		}
	}
	if blocked != 5 {
		t.Errorf("%d blocked-command notices, want 5", blocked)
	}
	// The same words in a normal app press the keys.
	r = dictRig(t, nil)
	r.session("Alfred write", "press enter")
	equal(t, "keys", r.input().Keys(), []string{"Enter"})
}

// The editor seam: nothing implements it yet.
func TestDictationTextEditorSeam(t *testing.T) {
	r := dictRig(t, nil)
	ctx := r.ctx()
	if err := r.p.ReplaceText(ctx, "a", "b"); !errors.Is(err, core.ErrNoSession) {
		t.Errorf("ReplaceText outside a session = %v, want ErrNoSession", err)
	}
	r.submit(core.TriggerWake, "Alfred write")
	if err := r.p.ReplaceText(ctx, "a", "b"); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("ReplaceText = %v, want ErrUnsupported", err)
	}
	if err := r.p.InsertAfterText(ctx, "a", "b"); !errors.Is(err, platform.ErrUnsupported) {
		t.Errorf("InsertAfterText = %v, want ErrUnsupported", err)
	}
	// "replace X with Y" is not understood yet: it is typed.
	r = dictRig(t, nil)
	r.session("Alfred write", "replace hello with goodbye")
	equal(t, "typed", r.typed(), []string{"replace hello with goodbye"})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// "write in a new note" creates a timestamped file, opens it, waits for the
// editor to take focus, then dictates.
func TestDictationNewNote(t *testing.T) {
	for _, c := range []struct{ say, name string }{
		{"Alfred write in a new note", "note 2026-11-27 14.30.12.txt"},
		{"Alfred, write in a new document.", "document 2026-11-27 14.30.12.md"},
		{"Alfred start a new note", "note 2026-11-27 14.30.12.txt"},
	} {
		t.Run(c.say, func(t *testing.T) {
			r := dictRig(t, nil)
			r.focusOnOpen()
			r.session(c.say, "meeting at nine", "stop dictation")
			path := filepath.Join(r.notes, c.name)
			if got := r.launcher().Opened(); len(got) != 1 || got[0] != (pfake.Opened{Kind: "file", Target: path}) {
				t.Errorf("opened %+v, want the new note %s", got, path)
			}
			if readFile(t, path) != "" {
				t.Error("the note is not empty")
			}
			equal(t, "typed", r.typed(), []string{"meeting at nine"})
		})
	}
}

// An editor that never takes focus: nothing is typed, and the user is told.
func TestDictationNewNoteFocusTimeout(t *testing.T) {
	r := dictRig(t, nil)
	r.submit(core.TriggerWake, "Alfred write in a new note")
	if r.p.Dictation().Active {
		t.Error("dictating into a window that never took focus")
	}
	if got := strings.Join(spoken(r.tts), " "); !strings.Contains(got, "never took focus") {
		t.Errorf("said %q", got)
	}
	if len(r.launcher().Opened()) != 1 {
		t.Errorf("opened %v", r.launcher().Opened())
	}
}

// "open <file> and write" with one match opens it and dictates into it.
func TestDictationOpenFile(t *testing.T) {
	finder := &fakeFinder{found: []string{"/home/me/Documents/budget.txt"}}
	r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Finder = finder })
	r.focusOnOpen()
	r.session("Alfred, open budget and write", "rent is due", "that's all")
	equal(t, "asked", finder.asked, []string{"budget"})
	if got := r.launcher().Opened(); len(got) != 1 || got[0].Target != "/home/me/Documents/budget.txt" {
		t.Errorf("opened %+v", got)
	}
	equal(t, "typed", r.typed(), []string{"rent is due"})

	// No match.
	finder = &fakeFinder{}
	r = dictRig(t, func(d *core.Deps, _ *core.Config) { d.Finder = finder })
	r.submit(core.TriggerWake, "Alfred open taxes and write")
	if got := spoken(r.tts); len(got) != 1 || !strings.Contains(got[0], "couldn't find a file called taxes") {
		t.Errorf("said %v", got)
	}
	if r.p.Dictation().Active {
		t.Error("session started without a file")
	}
}

// Several matches: the top three are read out and the user picks by number.
func TestDictationOpenFileAmbiguous(t *testing.T) {
	five := []string{"/h/a/budget.txt", "/h/b/budget.md", "/h/c/budget 2025.txt", "/h/d/old budget.txt", "/h/e/budget.csv"}
	finder := &fakeFinder{found: five}
	r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Finder = finder })
	r.submit(core.TriggerWake, "Alfred open budget and write")
	st := r.p.Dictation()
	if !st.Active || st.Phase != core.PhaseChoosing {
		t.Fatalf("Dictation() = %+v, want choosing", st)
	}
	got := spoken(r.tts)
	if len(got) != 1 {
		t.Fatalf("said %v", got)
	}
	for _, want := range []string{"5 files", "top three", "1, budget.txt, in a", "2, budget.md, in b", "3, budget 2025.txt, in c", "Which one?"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("reply %q lacks %q", got[0], want)
		}
	}
	if strings.Contains(got[0], "old budget") || strings.Contains(got[0], "4,") {
		t.Errorf("reply %q reads more than three", got[0])
	}
	if len(r.launcher().Opened()) != 0 {
		t.Error("opened a file before the user chose")
	}

	for _, c := range []struct {
		say  string
		want string
	}{{"two", "/h/b/budget.md"}, {"Number three.", "/h/c/budget 2025.txt"}, {"the first one", "/h/a/budget.txt"}} {
		t.Run(c.say, func(t *testing.T) {
			r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Finder = &fakeFinder{found: five} })
			r.focusOnOpen()
			r.session("Alfred open budget and write", c.say, "hello", "stop dictation")
			if got := r.launcher().Opened(); len(got) != 1 || got[0].Target != c.want {
				t.Errorf("opened %+v, want %s", got, c.want)
			}
			equal(t, "typed", r.typed(), []string{"hello"})
		})
	}
	// Something that is not an answer is waited through; "cancel" ends it.
	r = dictRig(t, func(d *core.Deps, _ *core.Config) { d.Finder = &fakeFinder{found: five} })
	status := bus.Subscribe(r.bus, core.DictationChanged, 8)
	r.session("Alfred open budget and write", "what", "five", "cancel", "hello")
	if len(r.launcher().Opened()) != 0 || len(r.typed()) != 0 {
		t.Errorf("opened %v and typed %v after a cancelled choice", r.launcher().Opened(), r.typed())
	}
	if st := await(t, status, func(s core.DictationStatus) bool { return !s.Active }); st.Reason != core.EndCancelled {
		t.Errorf("ended for %q, want cancelled", st.Reason)
	}
}

// Dictation by voice needs the always-on microphone.
func TestDictationNeedsWakeEngine(t *testing.T) {
	r := dictRig(t, func(d *core.Deps, _ *core.Config) { d.Wake = nil })
	r.submit(core.TriggerWake, "Alfred write")
	if r.p.Dictation().Active {
		t.Error("started without a wake engine")
	}
	if got := spoken(r.tts); len(got) != 1 || !strings.Contains(got[0], "always-on microphone") {
		t.Errorf("said %v", got)
	}
}

// "stop dictation" with no session says so rather than going to the AI.
func TestDictationStopWithoutSession(t *testing.T) {
	r := dictRig(t, nil)
	r.submit(core.TriggerWake, "Alfred stop dictation")
	if got := spoken(r.tts); len(got) != 1 || !strings.Contains(got[0], "not taking dictation") {
		t.Errorf("said %v", got)
	}
	if len(r.ai.Requests()) != 0 {
		t.Error("went to the AI")
	}
}

// The settings can change while a session runs.
func TestSetDictationLive(t *testing.T) {
	r := dictRig(t, nil)
	r.session("Alfred write", "over and out")
	equal(t, "typed", r.typed(), []string{"over and out"})
	r = dictRig(t, nil)
	r.p.SetDictation(core.DictationConfig{StopPhrases: []string{"over and out"}, IdleTimeout: time.Minute})
	r.session("Alfred write", "over and out", "after")
	equal(t, "typed", r.typed(), nil)
}
