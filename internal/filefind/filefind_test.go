package filefind

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func write(t *testing.T, root string, rel string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func rel(t *testing.T, root string, paths []string) []string {
	t.Helper()
	var out []string
	for _, p := range paths {
		r, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestFindRanksAndSkips(t *testing.T) {
	home := t.TempDir()
	old, recent := time.Unix(1_000_000, 0), time.Unix(2_000_000, 0)
	write(t, home, "Documents/meeting notes.txt", old)
	write(t, home, "Documents/Meeting-Notes-2025.md", recent)
	write(t, home, "Desktop/old meeting notes backup.txt", recent)
	write(t, home, "Documents/shopping.txt", old)
	write(t, home, ".cache/meeting notes.txt", recent)               // hidden folder
	write(t, home, "project/node_modules/meeting notes.txt", recent) // big folder
	write(t, home, ".hidden meeting notes.txt", recent)              // hidden file

	f := &Finder{Roots: []string{home}}
	got, err := f.Find(context.Background(), "Meeting Notes")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Documents/meeting notes.txt",          // exact
		"Documents/Meeting-Notes-2025.md",      // starts with it
		"Desktop/old meeting notes backup.txt", // contains it
	}
	if r := rel(t, home, got); !slices.Equal(r, want) {
		t.Errorf("Find = %v, want %v", r, want)
	}
}

func TestFindRecentFirstAndLimit(t *testing.T) {
	home := t.TempDir()
	mod := time.Unix(1_000_000, 0)
	a := write(t, home, "a/report.txt", mod.Add(time.Hour)) // newer
	b := write(t, home, "b/report.txt", mod)                // older, but recently used
	write(t, home, "c/report.txt", mod.Add(2*time.Hour))
	list := filepath.Join(home, "recently-used.xbel")
	xbel := `<xbel><bookmark href="file://` + b + `"/><bookmark href="file://` + home + `/.cache/x/report.txt"/></xbel>`
	if err := os.WriteFile(list, []byte(xbel), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &Finder{Roots: []string{home}, Recent: list, Limit: 2}
	got, err := f.Find(context.Background(), "report")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != b {
		t.Errorf("Find = %v, want the recently used %s first and 2 results", got, b)
	}
	_ = a
}

func TestFindBounded(t *testing.T) {
	home := t.TempDir()
	for _, n := range []string{"a1.txt", "a2.txt", "a3.txt", "a4.txt"} {
		write(t, home, n, time.Unix(1, 0))
	}
	// The visit limit stops the walk; what it found is returned, not an error.
	f := &Finder{Roots: []string{home}, MaxVisited: 2}
	got, err := f.Find(context.Background(), "a")
	if err != nil || len(got) >= 4 {
		t.Errorf("Find with MaxVisited 2 = %v, %v; want a partial result and no error", got, err)
	}
	// A cancelled context also gives no error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Finder{Roots: []string{home}}).Find(ctx, "a"); err != nil {
		t.Errorf("cancelled: %v", err)
	}
	if got, _ := (&Finder{Roots: []string{home}}).Find(context.Background(), "  "); got != nil {
		t.Errorf("empty query found %v", got)
	}
}
