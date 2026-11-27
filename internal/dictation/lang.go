package dictation

// Cmd is what a spoken command phrase does.
type Cmd int

// Commands.
const (
	CmdNewLine Cmd = iota + 1
	CmdNewParagraph
	CmdScratch
	CmdCapsOn
	CmdCapsOff
	CmdLiteral
	CmdCodeMode
	CmdTextMode
	CmdSpace
	CmdCapLetter
)

// Punct is a spoken punctuation mark or symbol.
type Punct struct {
	Text string
	// GlueLeft attaches it to the word before (no space before it);
	// GlueRight attaches it to the word after.
	GlueLeft, GlueRight bool
}

// Lang is one language's phrase lists. Phrases are lower case, without
// punctuation. Add a language by adding an entry to Langs.
type Lang struct {
	// Commands work in both modes.
	Commands map[string]Cmd
	// Punct is spoken punctuation in normal (text) mode.
	Punct map[string]Punct
	// Symbols are spoken symbols in code mode.
	Symbols map[string]string
}

// Langs holds the phrase lists by language code.
var Langs = map[string]*Lang{"en": english}

var left, right = Punct{GlueLeft: true}, Punct{GlueRight: true}

func lp(s string) Punct { p := left; p.Text = s; return p }
func rp(s string) Punct { p := right; p.Text = s; return p }

var english = &Lang{
	Commands: map[string]Cmd{
		"new line": CmdNewLine, "newline": CmdNewLine,
		"new paragraph": CmdNewParagraph,
		"scratch that":  CmdScratch, "delete that": CmdScratch,
		"all caps": CmdCapsOn, "end caps": CmdCapsOff,
		"literal":      CmdLiteral,
		"command mode": CmdCodeMode, "code mode": CmdCodeMode,
		"normal mode": CmdTextMode, "text mode": CmdTextMode,
		"space": CmdSpace, "cap": CmdCapLetter,
	},
	Punct: map[string]Punct{
		"period": lp("."), "full stop": lp("."), "comma": lp(","), "question mark": lp("?"),
		"exclamation mark": lp("!"), "exclamation point": lp("!"), "colon": lp(":"), "semicolon": lp(";"),
		"open quote": rp(`"`), "close quote": lp(`"`), "open paren": rp("("), "close paren": lp(")"),
		"open parenthesis": rp("("), "close parenthesis": lp(")"),
	},
	Symbols: map[string]string{
		"dash": "-", "double dash": "--", "dot": ".", "slash": "/", "backslash": `\`, "pipe": "|",
		"tilde": "~", "underscore": "_", "equals": "=", "plus": "+", "star": "*", "colon": ":",
		"at": "@", "hash": "#", "dollar": "$", "ampersand": "&", "greater than": ">", "less than": "<",
		"quote": `"`, "single quote": "'", "comma": ",", "semicolon": ";",
		"open paren": "(", "close paren": ")",
	},
}
