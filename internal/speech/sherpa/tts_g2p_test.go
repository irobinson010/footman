package sherpa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A few entries in each format, as the shipped lexicons have them.
const (
	testMisakiLexicon = `kettle k ˈ ɛ t ᵊ l
is ɪ z
on ˌ ɒ n
the ð ə
report ɹ ɪ p ˈ ɔ ː t
tidy t ˈ I d i
walk w ˈ ɔ ː k
want w ˈ ɒ n t
hot h ˈ ɒ t
key k ˈ i ː
butler b ˈ ʌ t l ə
`
	testEspeakLexicon = `# Lexicon: word(s) || space-separated phones
ashford || ˈ æ ʃ f ɚ d
klaus || k lˈ a ʊ s
the || ð ə
a while || ɐ   wˈ a ɪ l
go || ɡˈ oʊ
`
)

func testG2P(t *testing.T, misaki bool) *g2p {
	t.Helper()
	primary, err := parseLexicon(strings.NewReader(testMisakiLexicon))
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := parseLexicon(strings.NewReader(testEspeakLexicon))
	if err != nil {
		t.Fatal(err)
	}
	if !misaki {
		return newG2P(false, american, []lexicon{fallback}, nil, nil)
	}
	return newG2P(true, british, []lexicon{primary}, fallback, nil)
}

func TestParseLexicon(t *testing.T) {
	l, err := parseLexicon(strings.NewReader(testEspeakLexicon))
	if err != nil {
		t.Fatal(err)
	}
	if l["ashford"] != "ˈæʃfɚd" || l["klaus"] != "klˈaʊs" {
		t.Errorf("got %q", l)
	}
	if _, ok := l["a while"]; ok {
		t.Error("phrase kept")
	}
	if _, err := parseLexicon(strings.NewReader("# nothing\n")); err == nil {
		t.Error("empty lexicon accepted")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "lexicon-gb-en.txt")
	if err := os.WriteFile(p, []byte(testMisakiLexicon), 0o644); err != nil {
		t.Fatal(err)
	}
	if l, err := loadLexicon(p); err != nil || l["kettle"] != "kˈɛtᵊl" {
		t.Errorf("loadLexicon: %q, %v", l["kettle"], err)
	}
}

func TestParseTokens(t *testing.T) {
	known := parseTokens("; 1\n: 2\n  16\nA 24\nʤ 82\nab 99\n")
	for _, r := range []rune{';', ':', ' ', 'A', 'ʤ'} {
		if !known[r] {
			t.Errorf("%q missing", r)
		}
	}
	if len(known) != 5 {
		t.Errorf("got %d tokens", len(known))
	}
}

func TestPhonemize(t *testing.T) {
	g := testG2P(t, true)
	for _, c := range []struct{ in, want string }{
		{"The kettle is on.", "ðə kˈɛtᵊl ɪz ˌɒn."},
		// Missing from the Kokoro lexicon: from the espeak one, converted
		// (British: no r, ɚ becomes ə; aʊ becomes W).
		{"Ashford, Klaus!", "ˈæʃfəd, klˈWs!"},
		// Possessive, inflections and compounds of known words.
		{"Ashford's", "ˈæʃfədz"},
		{"tidied reports walked wanted keys", "tˈIdid ɹɪpˈɔːts wˈɔːkt wˈɒntɪd kˈiːz"},
		{"walking hotkey", "wˈɔːkɪŋ hˈɒt kˈiː"},
		// Short capitals are spelled; an unknown word is spelled, not
		// dropped.
		{"the API", "ðə ˈA pˈiː ˈI"},
		{"zq", "zˈiː kjˈuː"},
		// "a" is the article; "A" first in a sentence too.
		{"A butler on a key", "ɐ bˈʌtlə ˌɒn ɐ kˈiː"},
		// Hyphens split words; unknown punctuation is dropped or mapped.
		{"hot-key; on", "hˈɒt kˈiː; ˌɒn"},
	} {
		if got := string(g.phonemize(c.in)); got != c.want {
			t.Errorf("phonemize(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// Codepoints the model does not know are dropped.
	g.tokens = parseTokens("ð 1\nə 2\n  3\n")
	if got := string(g.phonemize("The the")); got != "ðə ðə" {
		t.Errorf("filtered: %q", got)
	}
	// Punctuation the model lacks is mapped to a comma or dropped.
	g.tokens = parseTokens("ð 1\nə 2\n  3\n, 4\n")
	if got := string(g.phonemize("the; the — the (the)")); got != "ðə, ðə, ðə ðə" {
		t.Errorf("punctuation: %q", got)
	}
}

func TestPhonemizeEspeak(t *testing.T) {
	g := testG2P(t, false)
	for _, c := range []struct{ in, want string }{
		{"Ashford go.", "ˈæʃfɚd ɡˈoʊ."},
		{"Klaus's", "klˈaʊsɪz"},
		{"the BBC", "ðə bˈiː bˈiː sˈiː"},
	} {
		if got := string(g.phonemize(c.in)); got != c.want {
			t.Errorf("phonemize(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestEspeakToMisaki(t *testing.T) {
	for _, c := range []struct {
		in   string
		acc  accent
		want string
	}{
		{"ˈæʃfɚd", british, "ˈæʃfəd"},
		{"ˈæʃfɚd", american, "ˈæʃfəɹd"},
		{"pˈɛɾɪɡɹˌuː", british, "pˈɛtɪɡɹˌuː"},
		{"pˈɛɾɪɡɹˌuː", american, "pˈɛTɪɡɹˌu"},
		{"ɡˈoʊ", british, "ɡˈQ"},
		{"ɡˈoʊ", american, "ɡˈO"},
		{"dʒˈeɪ tʃˈɔɪs", american, "ʤˈA ʧˈYs"},
		{"hˈɪɹ", british, "hˈɪə"},
		{"kˈɑːɹt", british, "kˈɑːt"},
		{"ɹᵻpˈoːɹt", british, "ɹɪpˈɔːt"},
	} {
		if got := espeakToMisaki(c.in, c.acc); got != c.want {
			t.Errorf("espeakToMisaki(%q, %d) = %q, want %q", c.in, c.acc, got, c.want)
		}
	}
}

func TestTokenize(t *testing.T) {
	var got []string
	for _, tk := range tokenize(`"Don't," he said; the butlers' co-op (3x)...`) {
		got = append(got, tk.text)
	}
	want := `" Don't , " he said ; the butlers co op ( 3x ) . . .`
	if strings.Join(got, " ") != want {
		t.Errorf("got  %q\nwant %q", strings.Join(got, " "), want)
	}
}

func TestCamelParts(t *testing.T) {
	for in, want := range map[string]string{"OpenAI": "Open AI", "iPhone": "i Phone", "HTTPServer": "HTTP Server", "plain": "plain"} {
		if got := strings.Join(camelParts(in), " "); got != want {
			t.Errorf("camelParts(%q) = %q", in, got)
		}
	}
}
