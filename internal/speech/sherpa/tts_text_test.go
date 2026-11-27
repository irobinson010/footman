package sherpa

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"It is 7:45.", "It is seven forty-five."},
		{"Meet at 9:05 pm. At 4 p.m. sharp.", "Meet at nine oh five p m. At four p m sharp."},
		{"Lunch at 12:00, tea at 4pm.", "Lunch at twelve o'clock, tea at four p m."},
		{"The train leaves at 10:00.", "The train leaves at ten o'clock."},
		{"Boarding 14:30.", "Boarding fourteen thirty."},
		{"I have 3 reports.", "I have three reports."},
		{"It costs £42.50.", "It costs forty-two pounds and fifty pence."},
		{"Only $1 or $0.99.", "Only one dollar or ninety-nine cents."},
		{"A €2.5m budget.", "A two point five million euros budget."},
		{"Up 15% today.", "Up fifteen percent today."},
		{"The 3rd and 21st of May.", "The third and twenty-first of May."},
		{"1,234,567 files.", "one million two hundred and thirty-four thousand five hundred and sixty-seven files."},
		{"Pi is 3.14.", "Pi is three point one four."},
		{"In 1984 and 2026 and 2005.", "In nineteen eighty-four and twenty twenty-six and two thousand and five."},
		{"Call 07700900123.", "Call oh seven seven oh oh nine oh oh one two three."},
		{"It was -5 outside, 20°C inside.", "It was minus five outside, twenty degrees Celsius inside."},
		{"A 16GB card, 120 ms.", "A sixteen gigabytes card, one hundred and twenty milliseconds."},
		{"Pages 3-5.", "Pages three to five."},
		{"Mr. Smith and Dr. Jones.", "Mister Smith and Doctor Jones."},
		{"Eggs, milk, etc. Then home.", "Eggs, milk, et cetera. Then home."},
		{"Tools e.g. hammers.", "Tools for example hammers."},
		{"On 2026-11-26.", "On November twenty-sixth, twenty twenty-six."},
		{"Rock & roll.", "Rock and roll."},
		{"See https://www.example.com/a/b?c=1 now.", "See example dot com now."},
		{"Mail alex@example.org.", "Mail alex at example dot org."},
		{"An RTX4080.", "An RTX four thousand and eighty."},
		{"Version 2.0.3.", "Version two point zero point three."},
		{"Half: 1/2, a quarter: 1/4, 2/3.", "Half: one half, a quarter: one quarter, two thirds."},
		{"Room #4, No. 7.", "Room number four, number seven."},
		{"**Bold** and `code`.", "Bold and code."},
		{"“Quotes” — dashes…", `"Quotes", dashes...`},
	} {
		if got := NormalizeText(c.in); got != c.want {
			t.Errorf("NormalizeText(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestNumberWords(t *testing.T) {
	for n, want := range map[int64]string{
		0: "zero", 13: "thirteen", 40: "forty", 99: "ninety-nine", 100: "one hundred",
		105: "one hundred and five", 1000: "one thousand", 1001: "one thousand and one",
		2500: "two thousand five hundred", 1_000_000: "one million",
	} {
		if got := cardinalWords(n); got != want {
			t.Errorf("cardinalWords(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int]string{1: "first", 2: "second", 3: "third", 5: "fifth", 12: "twelfth",
		20: "twentieth", 22: "twenty-second", 100: "one hundredth"} {
		if got := ordinalWords(n); got != want {
			t.Errorf("ordinalWords(%d) = %q, want %q", n, got, want)
		}
	}
	for y, want := range map[string]string{"1900": "nineteen hundred", "1905": "nineteen oh five",
		"2000": "two thousand", "2010": "twenty ten", "1999": "nineteen ninety-nine"} {
		if got := yearWords(y); got != want {
			t.Errorf("yearWords(%s) = %q, want %q", y, got, want)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	text := "Very good, sir. Shall I proceed? I have tidied the study, filed the reports, and booked the train!\nNew line here"
	got := splitSentences(text, -1, 25)
	want := []string{"Very good, sir.", "Shall I proceed?", "I have tidied the study, filed the reports, and booked the train!", "New line here"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
	// The first sentence is cut at its first pause after 4 words.
	got = splitSentences("I have tidied the study, filed the reports, and booked the train. Done.", 4, 25)
	want = []string{"I have tidied the study,", "filed the reports, and booked the train.", "Done."}
	if !slices.Equal(got, want) {
		t.Errorf("first cut: got %q", got)
	}
	// Too short to leave three words after the cut: not cut.
	if got := splitSentences("Right away, sir.", 1, 25); len(got) != 1 {
		t.Errorf("short: got %q", got)
	}
	// A long sentence is cut at pauses, and where it has none.
	long := strings.Repeat("word ", 15) + "pause, " + strings.Repeat("word ", 50) + "end."
	pieces := splitSentences(long, -1, 20)
	if len(pieces) < 3 {
		t.Fatalf("long: got %d pieces", len(pieces))
	}
	if !strings.HasSuffix(pieces[0], "pause,") {
		t.Errorf("long: first piece %q", pieces[0])
	}
	for _, p := range pieces {
		if n := len(strings.Fields(p)); n > 30 {
			t.Errorf("piece of %d words", n)
		}
	}
	if strings.Join(pieces, " ") != strings.Join(strings.Fields(long), " ") {
		t.Error("pieces lost words")
	}
	// Quotes and repeated punctuation stay with their sentence; nothing
	// without a word becomes a piece.
	got = splitSentences(`He said "stop!" Then... nothing. --- ?!`, -1, 25)
	want = []string{`He said "stop!"`, "Then...", "nothing."}
	if !slices.Equal(got, want) {
		t.Errorf("punctuation: got %q", got)
	}
}
