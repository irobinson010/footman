package sherpa

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDumpPiperPhonemes is a training-data tool, not a test: with
// FOOTMAN_PHONEMES_IN (a JSONL manifest with "id" and "text") and
// FOOTMAN_PHONEMES_OUT set, it writes one "id|normalized text|phonemes" line
// per manifest row, phonemized exactly as the engine does for a stock Piper
// model (NormalizeText, then the espeak-IPA lexicon, keeping only codepoints in
// the model's tokens.txt). The fine-tuned Ashford voice is trained on these
// phonemes so training matches what Footman feeds it at run time (S0-10).
// FOOTMAN_PHONEMES_MODELS is the models folder (default: testModelsDir's
// search); FOOTMAN_PHONEMES_EXTRA is a lexicon tried first, like the lexicon.txt
// shipped beside the voice; FOOTMAN_PHONEMES_KEEP=1 keeps only rows with verdict "keep".
func TestDumpPiperPhonemes(t *testing.T) {
	in, out := os.Getenv("FOOTMAN_PHONEMES_IN"), os.Getenv("FOOTMAN_PHONEMES_OUT")
	if in == "" || out == "" {
		t.Skip("FOOTMAN_PHONEMES_IN / FOOTMAN_PHONEMES_OUT not set")
	}
	dir := os.Getenv("FOOTMAN_PHONEMES_MODELS")
	if dir == "" {
		dir = testModelsDir()
	}
	fallback, err := loadLexicon(filepath.Join(dir, "lexicon-en-us.txt"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "vits-piper-en_US-norman-medium", "tokens.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var primary []lexicon
	if extra := os.Getenv("FOOTMAN_PHONEMES_EXTRA"); extra != "" {
		l, err := loadLexicon(extra)
		if err != nil {
			t.Fatal(err)
		}
		primary = append(primary, l)
	}
	g := newG2P(false, american, primary, fallback, parseTokens(string(raw)))
	var spelled []string
	g.onSpell = func(w string) { spelled = append(spelled, w) }

	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	o, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	w := bufio.NewWriter(o)
	defer func() { _ = w.Flush() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		var row struct{ ID, Text, Verdict string }
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("FOOTMAN_PHONEMES_KEEP") == "1" && row.Verdict != "keep" {
			continue
		}
		norm := NormalizeText(row.Text)
		ph := string(g.phonemize(norm))
		if strings.ContainsAny(norm+ph, "|\n") {
			t.Fatalf("%s: separator in %q / %q", row.ID, norm, ph)
		}
		_, _ = fmt.Fprintf(w, "%s|%s|%s\n", row.ID, norm, ph)
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d lines; %d words spelled letter by letter: %v", n, len(spelled), spelled)
}
