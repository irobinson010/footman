package dictation_test

import (
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/dictation"
)

func TestFormatText(t *testing.T) {
	f := dictation.NewFormatter("en")
	cases := []struct{ in, want string }{
		{"hello world", "hello world"},
		{"Dear Sam, new paragraph thanks", "Dear Sam,\n\nthanks"},
		{"Dear Sam, New paragraph. Thanks for the note.", "Dear Sam,\n\nThanks for the note."},
		{"line one. New line. Line two", "line one.\nLine two"},
		{"Line one new line line two", "Line one\nline two"},
		{"hello period", "hello."},
		{"Hello, period. How are you question mark", "Hello. How are you?"},
		{"Wow exclamation mark", "Wow!"},
		{"dear sam comma", "dear sam,"},
		{"note colon buy milk semicolon eggs", "note: buy milk; eggs"},
		{"he said open quote hi close quote to me", `he said "hi" to me`},
		{"see open paren below close paren now", "see (below) now"},
		{"this is all caps very important end caps stuff", "this is VERY IMPORTANT stuff"},
		{"all caps shout", "SHOUT"},
		{"One. Two. Scratch that.", "One."},
		{"First idea. Bad idea scratch that good idea", "First idea. good idea"},
		// the literal escape types the words
		{"literal new line", "new line"},
		{"say literal new paragraph please", "say new paragraph please"},
		{"type literal period now", "type period now"},
		{"the newline character", "the\ncharacter"}, // "newline" is accepted as a spelling
	}
	for _, c := range cases {
		got := f.Format(c.in, dictation.FormatState{}).Text
		if got != c.want {
			t.Errorf("Format(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatScratchPrev(t *testing.T) {
	f := dictation.NewFormatter("en")
	r := f.Format("Scratch that.", dictation.FormatState{})
	if !r.ScratchPrev || r.Text != "" {
		t.Errorf("got %+v, want ScratchPrev and no text", r)
	}
	if r := f.Format("hello scratch that", dictation.FormatState{}); r.ScratchPrev || r.Text != "" {
		t.Errorf("in-utterance scratch: got %+v", r)
	}
}

func TestFormatCode(t *testing.T) {
	f := dictation.NewFormatter("en")
	cases := []struct{ in, want string }{
		{"PS space dash A U X", "ps -aux"},
		{"ps space dash aux", "ps -aux"},
		{"Ps space dash a u x.", "ps -aux"},
		{"ps space dash A.U.X.", "ps -aux"},
		{"ls space double dash all", "ls --all"},
		{"cat space file dot txt space pipe space grep space foo", "cat file.txt | grep foo"},
		{"git commit space dash m", "git commit -m"},
		{"cd space tilde slash src", "cd ~/src"},
		{"export space foo equals bar", "export foo=bar"},
		{"echo space dollar home", "echo $home"},
		{"echo space cap A b", "echo Ab"},
		{"my underscore file", "my_file"},
		{"user at host dot com", "user@host.com"},
		{"a ampersand b greater than c", "a&b>c"},
		{"echo space quote hi quote", `echo "hi"`},
		{"echo space single quote hi single quote", "echo 'hi'"},
		{"um ls", "ls"},
		{"ls space dash dash help", "ls --help"},
		{"ls space dash l", "ls -l"},
		{"Ls.", "ls"}, // no capital, no trailing period
		{"ls literal dash", "ls dash"},
	}
	for _, c := range cases {
		got := f.Format(c.in, dictation.FormatState{Code: true, Terminal: true}).Text
		if got != c.want {
			t.Errorf("code Format(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatModes(t *testing.T) {
	f := dictation.NewFormatter("en")
	r := f.Format("Command mode. ps space dash aux", dictation.FormatState{})
	if r.Text != "ps -aux" || !r.Code {
		t.Errorf("got %+v", r)
	}
	r = f.Format("normal mode Hello there", dictation.FormatState{Code: true})
	if r.Text != "Hello there" || r.Code {
		t.Errorf("got %+v", r)
	}
	if r := f.Format("code mode", dictation.FormatState{}); r.Text != "" || !r.Code {
		t.Errorf("got %+v", r)
	}
	if r := f.Format("text mode", dictation.FormatState{Code: true}); r.Text != "" || r.Code {
		t.Errorf("got %+v", r)
	}
}

// A terminal never gets Enter: that would run the command.
func TestFormatNeverEnterInTerminal(t *testing.T) {
	f := dictation.NewFormatter("en")
	for _, code := range []bool{true, false} {
		for _, in := range []string{
			"ls new line", "New line. ls", "echo new paragraph hi", "ls space dash l new line new paragraph",
			"normal mode ls new line", "code mode ls New paragraph.",
		} {
			r := f.Format(in, dictation.FormatState{Terminal: true, Code: code})
			if strings.ContainsAny(r.Text, "\r\n") {
				t.Errorf("Format(%q, code=%v) = %q has a line break", in, code, r.Text)
			}
		}
	}
	if got := f.Format("ls new line", dictation.FormatState{Terminal: true, Code: true}).Text; got != "ls" {
		t.Errorf("got %q, want ls", got)
	}
	// Outside a terminal, code mode may press Enter.
	if got := f.Format("ls new line", dictation.FormatState{Code: true}).Text; got != "ls\n" {
		t.Errorf("got %q", got)
	}
}

func TestFormatUnknownLanguageIsEnglish(t *testing.T) {
	if got := dictation.NewFormatter("xx").Format("hi period", dictation.FormatState{}).Text; got != "hi." {
		t.Errorf("got %q", got)
	}
}
