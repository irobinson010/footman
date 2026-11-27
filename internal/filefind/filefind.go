// Package filefind finds a file by the name a person says, for "open <file>
// and write" (ADR-0045). The search is bounded: it reads the desktop's list of
// recently used files first, then walks the home folder, skipping hidden
// folders and big ones, and stops at a time limit or a visit limit, whichever
// comes first, returning what it has.
package filefind

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Defaults for a Finder's limits.
const (
	DefaultTimeout    = 2 * time.Second
	DefaultMaxVisited = 200_000
	DefaultLimit      = 10
)

// skipDirs are folders that hold many files nobody dictates into.
var skipDirs = map[string]bool{
	"node_modules": true, "__pycache__": true, "site-packages": true, "venv": true,
	"vendor": true, "snap": true, "steamapps": true, "target": true, "dist": true,
	"build": true, "trash": true,
}

// Finder searches for files by spoken name. The zero value searches nothing;
// use New.
type Finder struct {
	// Roots are the folders searched, usually just the home folder.
	Roots []string
	// Recent is the path of the desktop's recently-used list
	// (recently-used.xbel); empty means none.
	Recent string
	// Timeout caps the whole search (DefaultTimeout), MaxVisited the entries
	// looked at (DefaultMaxVisited) and Limit the results (DefaultLimit).
	Timeout    time.Duration
	MaxVisited int
	Limit      int
}

// New returns a Finder for home, using the freedesktop recently-used list.
func New(home string) *Finder {
	return &Finder{
		Roots:  []string{home},
		Recent: filepath.Join(home, ".local", "share", "recently-used.xbel"),
	}
}

type hit struct {
	path   string
	score  int // 0 exact, 1 prefix, 2 contains, 3 every word present
	recent bool
	mod    time.Time
}

// Find returns the files whose name matches query (spoken words such as
// "meeting notes", which also finds "Meeting-Notes.txt"), best first: exact
// names, then names that start with it, then names that contain it, and
// within each the recently used, then the newest. The search stops at the
// time or visit limit and returns what it found; an empty result is not an
// error.
func (f *Finder) Find(ctx context.Context, query string) ([]string, error) {
	words := splitWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	timeout, maxVisited, limit := f.Timeout, f.MaxVisited, f.Limit
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxVisited <= 0 {
		maxVisited = DefaultMaxVisited
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	seen := map[string]*hit{}
	add := func(path string, recent bool) {
		if h, ok := seen[path]; ok {
			h.recent = h.recent || recent
			return
		}
		score, ok := match(words, filepath.Base(path))
		if !ok {
			return
		}
		h := &hit{path: path, score: score, recent: recent}
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			h.mod = fi.ModTime()
		} else {
			return
		}
		seen[path] = h
	}
	for _, p := range f.recentFiles() {
		add(p, true)
	}
	visited := 0
	for _, root := range f.Roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if visited++; visited > maxVisited || ctx.Err() != nil {
				return fs.SkipAll
			}
			name := d.Name()
			if path != root && strings.HasPrefix(name, ".") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && skipDirs[strings.ToLower(name)] {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				add(path, false)
			}
			return nil
		})
	}
	hits := make([]*hit, 0, len(seen))
	for _, h := range seen {
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		switch {
		case a.score != b.score:
			return a.score < b.score
		case a.recent != b.recent:
			return a.recent
		case !a.mod.Equal(b.mod):
			return a.mod.After(b.mod)
		}
		return a.path < b.path
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.path
	}
	return out, nil
}

var hrefRE = regexp.MustCompile(`href="file://([^"]+)"`)

// recentFiles returns the paths in the recently-used list, newest last as the
// file keeps them; unreadable or missing lists give none.
func (f *Finder) recentFiles() []string {
	if f.Recent == "" {
		return nil
	}
	b, err := os.ReadFile(f.Recent)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range hrefRE.FindAllStringSubmatch(string(b), -1) {
		p, err := url.PathUnescape(m[1])
		if err != nil || !filepath.IsAbs(p) {
			continue
		}
		if f.underRoots(p) && !hiddenPath(p) {
			out = append(out, p)
		}
	}
	return out
}

func (f *Finder) underRoots(p string) bool {
	for _, r := range f.Roots {
		if rel, err := filepath.Rel(r, p); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}

func hiddenPath(p string) bool {
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

// splitWords lower-cases s and splits it into runs of letters and digits.
func splitWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// match scores a file name against the query's words (see hit.score).
func match(words []string, name string) (int, bool) {
	want := strings.Join(words, "")
	withExt := strings.Join(splitWords(name), "")
	bare := strings.Join(splitWords(strings.TrimSuffix(name, filepath.Ext(name))), "")
	switch {
	case bare == want || withExt == want:
		return 0, true
	case strings.HasPrefix(bare, want):
		return 1, true
	case strings.Contains(bare, want) || strings.Contains(withExt, want):
		return 2, true
	}
	have := map[string]bool{}
	for _, w := range splitWords(name) {
		have[w] = true
	}
	for _, w := range words {
		if !have[w] {
			return 0, false
		}
	}
	return 3, true
}
