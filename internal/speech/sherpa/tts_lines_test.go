package sherpa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/personas"
)

// testModelsDir finds a models folder holding the Kokoro lexicons: $FOOTMAN_TEST_MODELS,
// then the usual data folders. It returns "" if none has them.
func testModelsDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{
		os.Getenv("FOOTMAN_TEST_MODELS"),
		filepath.Join(home, ".local/share/footman/models"),
		filepath.Join(home, "PersonalProjects/footman-data/models"),
	} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "kokoro-multi-lang-v1_0", "lexicon-gb-en.txt")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "lexicon-en-us.txt")); err == nil {
				return d
			}
		}
	}
	return ""
}

func realKokoroG2P(t *testing.T, dir string, acc accent) *g2p {
	t.Helper()
	kd := filepath.Join(dir, "kokoro-multi-lang-v1_0")
	name := "lexicon-us-en.txt"
	if acc == british {
		name = "lexicon-gb-en.txt"
	}
	primary, err := loadLexicon(filepath.Join(kd, name))
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := loadLexicon(filepath.Join(dir, "lexicon-en-us.txt"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(kd, "tokens.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return newG2P(true, acc, []lexicon{primary}, fallback, parseTokens(string(raw)))
}

// TestShippedLinesNeverSpelled runs the G2P on every line of every shipped
// persona pack, with the pack's respellings, and fails if any word would be
// spelled out letter by letter (ADR-0037 addendum). It needs the Kokoro and
// espeak lexicons, so it skips without them.
func TestShippedLinesNeverSpelled(t *testing.T) {
	dir := testModelsDir()
	if dir == "" {
		t.Skip("no models folder with the Kokoro lexicons (set $FOOTMAN_TEST_MODELS)")
	}
	cat := persona.NewCatalog(personas.FS, t.TempDir())
	for _, info := range cat.List() {
		p := info.Pack
		if p == nil {
			t.Fatalf("pack %s did not load: %v", info.ID, info.Err)
		}
		acc := chooseAccent("", p.Voice.Speaker, "")
		g := realKokoroG2P(t, dir, acc)
		var spelled []string
		g.onSpell = func(w string) { spelled = append(spelled, w) }
		rs := newRespeller(p.Pronounce)
		for _, lang := range p.Languages {
			for _, k := range persona.Kinds {
				for _, l := range p.Lines(k, lang) {
					text := rs.apply(NormalizeText(l.Render("Herr Alex")))
					spelled = spelled[:0]
					for _, piece := range splitSentences(text, 0, 25) {
						if len(g.phonemize(piece)) == 0 {
							t.Errorf("%s %s: %q phonemizes to nothing", p.ID, k, l.Text)
						}
					}
					if len(spelled) > 0 {
						t.Errorf("%s %s line %q: spelled letter by letter: %s (add a [pronounce] entry)",
							p.ID, k, l.Text, strings.Join(spelled, ", "))
					}
				}
			}
		}
	}
}

func TestRespeller(t *testing.T) {
	r := newRespeller(map[string]string{
		"jawohl":          "yah vole",
		"es tut mir leid": "ess toot meer lide",
		"es":              "ess",
		"gut":             "goot",
	})
	for _, c := range []struct{ in, want string }{
		{"Jawohl, Herr Alex.", "yah vole, Herr Alex."},
		{"JAWOHL!", "yah vole!"},
		{"Es tut  mir leid, sir.", "ess toot meer lide, sir."},
		{"Es geht. Gut.", "ess geht. goot."},
		{"Guten Gutenberg regut", "Guten Gutenberg regut"},
		{"Es tut mir", "ess tut mir"},
		{"", ""},
	} {
		if got := r.apply(c.in); got != c.want {
			t.Errorf("apply(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if newRespeller(nil).apply("x") != "x" {
		t.Error("nil respeller changed text")
	}
}
