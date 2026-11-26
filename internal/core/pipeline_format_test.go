package core_test

import (
	"testing"

	"github.com/irobinson010/footman/internal/core"
)

func TestDictationFormatsText(t *testing.T) {
	r := newRig(t, nil)
	r.submit(core.TriggerDictate, "Dear Sam, New paragraph. Thanks period")
	all := append(r.input().Typed(), r.input().Pasted()...)
	equal(t, "inserted", all, []string{"Dear Sam,\n\nThanks."})
}

func TestDictationCommandsOff(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.NoCommands = true })
	r.submit(core.TriggerDictate, "hello new line world")
	equal(t, "typed", r.input().Typed(), []string{"hello new line world"})
}

// In a terminal: code mode, and never Enter (ADR-0032).
func TestDictationTerminal(t *testing.T) {
	r := newRig(t, nil)
	r.input().Terminal = true
	r.submit(core.TriggerDictate, "PS space dash A U X")
	r.submit(core.TriggerDictate, "New line.")
	equal(t, "typed", r.input().Typed(), []string{"ps -aux"})
	equal(t, "pasted", r.input().Pasted(), nil)
}

func TestDictationTerminalModeOff(t *testing.T) {
	r := newRig(t, func(_ *core.Deps, c *core.Config) { c.NoTerminal = true })
	r.input().Terminal = true
	r.submit(core.TriggerDictate, "hello world new line")
	equal(t, "typed", r.input().Typed(), []string{"hello world"}) // still no Enter
}

func TestDictationSpokenCodeMode(t *testing.T) {
	r := newRig(t, nil)
	r.submit(core.TriggerDictate, "code mode")
	r.submit(core.TriggerDictate, "ls space dash l")
	r.submit(core.TriggerDictate, "normal mode")
	r.submit(core.TriggerDictate, "Hello there")
	equal(t, "typed", r.input().Typed(), []string{"ls -l", "Hello there"})
}

func TestDictationScratchThat(t *testing.T) {
	r := newRig(t, nil)
	r.submit(core.TriggerDictate, "hello world")
	r.submit(core.TriggerDictate, "Scratch that.")
	r.submit(core.TriggerDictate, "scratch that")
	equal(t, "backspaces", r.input().Backspaces(), []int{11}) // the second has nothing to take back
}
