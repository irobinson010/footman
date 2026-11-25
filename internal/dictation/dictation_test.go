package dictation_test

import (
	"testing"

	"github.com/irobinson010/footman/internal/dictation"
)

func TestCleaner(t *testing.T) {
	cl := dictation.NewCleaner(nil, nil)
	cases := []struct{ in, want string }{
		{"hello world", "hello world"},
		{"Um, hello hello world", "hello world"},
		{"so uh I think, er, the the plan works", "so I think, the plan works"},
		{"that's it, um.", "that's it."},
		{"no, no, really", "no, no, really"}, // a comma marks a deliberate repeat
		{"Dear Sam, new paragraph thanks for the note", "Dear Sam,\n\nthanks for the note"},
		{"line one new line line two", "line one\nline two"},
		{"First idea. Bad idea scratch that good idea", "First idea. good idea"},
		{"One. Two. Scratch that.", "One."},
		{"new line scratch that hello", "hello"},
		{"scratch that", ""},
		{"um uh", ""},
	}
	for _, c := range cases {
		if got := cl.Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanerIsExtensible(t *testing.T) {
	c := dictation.NewCleaner([]string{"like"}, append(dictation.DefaultDictationCommands,
		dictation.DictationCommand{Phrase: "full stop", Apply: func(d *dictation.Draft) { d.Word(".") }}))
	if got, want := c.Clean("it was like um great full stop"), "it was um great ."; got != want {
		t.Errorf("Clean = %q, want %q", got, want)
	}
}

func TestChooseInsert(t *testing.T) {
	l := dictation.ASCIILayout{}
	cases := []struct {
		mode dictation.InsertMode
		text string
		want dictation.InsertMethod
	}{
		{dictation.InsertAuto, "hello world", dictation.MethodType},
		{dictation.InsertAuto, "two\nlines", dictation.MethodPaste},
		{dictation.InsertAuto, "café", dictation.MethodPaste},
		{dictation.InsertAuto, string(make([]byte, 201)), dictation.MethodPaste},
		{dictation.InsertType, "two\nlines", dictation.MethodType},
		{dictation.InsertPaste, "hi", dictation.MethodPaste},
	}
	for _, c := range cases {
		if got := dictation.ChooseInsert(c.mode, c.text, l, dictation.DefaultTypeMax); got != c.want {
			t.Errorf("dictation.ChooseInsert(%s, %.20q) = %s, want %s", c.mode, c.text, got, c.want)
		}
	}
}
