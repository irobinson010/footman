package core

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func split(t *testing.T, chunks ...string) []string {
	t.Helper()
	s := NewSplitter("en")
	var out []string
	for _, c := range chunks {
		out = append(out, s.Push(c)...)
	}
	return append(out, s.Flush()...)
}

func TestSplitter(t *testing.T) {
	long := strings.Repeat("word ", 30) + "and, " + strings.Repeat("more ", 30) + "end"
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "Hello there. How are you? Fine!", []string{"Hello there.", "How are you?", "Fine!"}},
		{"titles", "Dr. Smith met Mr. Jones.", []string{"Dr. Smith met Mr. Jones."}},
		{"eg", "Use fruit, e.g. apples. Or pears.", []string{"Use fruit, e.g. apples.", "Or pears."}},
		{"initials", "J. R. R. Tolkien wrote it. Good.", []string{"J. R. R. Tolkien wrote it.", "Good."}},
		{"decimals", "It costs 3.50 today. Cheap.", []string{"It costs 3.50 today.", "Cheap."}},
		{"time", "It is 9.30 now. Go.", []string{"It is 9.30 now.", "Go."}},
		{"pm lower", "At 9 p.m. tonight, then bed.", []string{"At 9 p.m. tonight, then bed."}},
		{"pm upper", "We met at 9 p.m. Then we left.", []string{"We met at 9 p.m.", "Then we left."}},
		{"ellipsis lower", "Well... maybe so. Fine.", []string{"Well... maybe so.", "Fine."}},
		{"ellipsis upper", "Well... Maybe so.", []string{"Well...", "Maybe so."}},
		{"unicode ellipsis", "Hmm… perhaps. Yes.", []string{"Hmm… perhaps.", "Yes."}},
		{"quote", `He said "stop." Then left.`, []string{`He said "stop."`, "Then left."}},
		{"list number", "1. Eggs\n2. Milk", []string{"1. Eggs", "2. Milk"}},
		{"newline", "Heading\nBody text here", []string{"Heading", "Body text here"}},
		{"url", "Visit example.com today. Bye.", []string{"Visit example.com today.", "Bye."}},
		{"no end", "No full stop", []string{"No full stop"}},
		{"fence", "Try:\n```go\nx := 1. 2.\n```\nDone.", []string{"Try:", "```go\nx := 1. 2.\n```", "Done."}},
		{"unclosed fence", "```\ncode only", []string{"```\ncode only"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := split(t, tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("split(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The same text arriving a few characters at a time splits the same.
			var parts []string
			for r := range strings.SplitSeq(tc.in, "") {
				parts = append(parts, r)
			}
			if got := split(t, parts...); !slices.Equal(got, tc.want) {
				t.Errorf("split(%q) streamed = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	t.Run("long sentence", func(t *testing.T) {
		got := split(t, long)
		if len(got) < 2 {
			t.Fatalf("long sentence not split: %q", got)
		}
		for _, s := range got {
			if len([]rune(s)) > maxSentence {
				t.Errorf("chunk of %d runes exceeds the cap: %q", len([]rune(s)), s)
			}
		}
		if !strings.HasSuffix(got[0], "and,") {
			t.Errorf("first chunk %q should end at the clause", got[0])
		}
		if joined := strings.Join(got, " "); joined != long {
			t.Errorf("text changed:\n got %q\nwant %q", joined, long)
		}
	})
	t.Run("waits for the end", func(t *testing.T) {
		s := NewSplitter("en")
		if got := s.Push("Hello there."); len(got) != 0 {
			t.Errorf("emitted %q before knowing the sentence ended", got)
		}
		if got := s.Push(" Next"); !slices.Equal(got, []string{"Hello there."}) {
			t.Errorf("got %q", got)
		}
	})
}

func TestSpeakableGolden(t *testing.T) {
	ins, _ := filepath.Glob("testdata/speakable/*.in")
	if len(ins) == 0 {
		t.Fatal("no golden inputs")
	}
	for _, in := range ins {
		t.Run(filepath.Base(in), func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, c := range split(t, string(src)) {
				if s := Speakable(c); s != "" {
					lines = append(lines, s)
				}
			}
			got := strings.Join(lines, "\n") + "\n"
			golden := strings.TrimSuffix(in, ".in") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("speakable text differs (run with -update to accept):\n--- got\n%s--- want\n%s", got, want)
			}
		})
	}
}

func TestInQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 26, h, m, 0, 0, time.Local) }
	tests := []struct {
		spec string
		t    time.Time
		want bool
	}{
		{"", at(23, 0), false},
		{"nonsense", at(23, 0), false},
		{"22:00-07:00", at(22, 0), true},
		{"22:00-07:00", at(23, 59), true},
		{"22:00-07:00", at(0, 0), true},
		{"22:00-07:00", at(6, 59), true},
		{"22:00-07:00", at(7, 0), false},
		{"22:00-07:00", at(12, 0), false},
		{"22:00-07:00", at(21, 59), false},
		{"13:00-14:30", at(13, 0), true},
		{"13:00-14:30", at(14, 30), false},
		{"13:00-14:30", at(9, 0), false},
		{"08:00-08:00", at(8, 0), false},
	}
	for _, tc := range tests {
		if got := InQuietHours(tc.spec, tc.t); got != tc.want {
			t.Errorf("InQuietHours(%q, %s) = %v, want %v", tc.spec, tc.t.Format("15:04"), got, tc.want)
		}
	}
}
