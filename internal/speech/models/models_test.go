package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func serve(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestFetchRequires(t *testing.T) {
	lex, voice := []byte("hello h e l o"), []byte("voice weights")
	srv := serve(t, map[string][]byte{"/lex.txt": lex, "/voice.onnx": voice})
	dep := Model{ID: "test-lexicon", Kind: "lexicon", File: "lex.txt", URL: srv.URL + "/lex.txt", SHA256: sum(lex), Size: int64(len(lex))}
	saved := Catalog
	Catalog = append(Catalog[:len(Catalog):len(Catalog)], dep)
	t.Cleanup(func() { Catalog = saved })

	dir := t.TempDir()
	m := Model{ID: "test-voice", Kind: "tts", File: "voice.onnx", URL: srv.URL + "/voice.onnx", SHA256: sum(voice), Size: int64(len(voice)), Requires: []string{dep.ID}}
	var recorded []string
	if _, err := Fetch(context.Background(), m, dir, FetchOptions{Record: func(p string, _ bool) error { recorded = append(recorded, p); return nil }}); err != nil {
		t.Fatal(err)
	}
	if !dep.Installed(dir) || !m.Installed(dir) {
		t.Error("the voice or its lexicon is missing")
	}
	if !slices.Contains(recorded, dep.Path(dir)) {
		t.Errorf("lexicon not recorded: %v", recorded)
	}
	m.Requires = []string{"no-such-model"}
	if _, err := Fetch(context.Background(), m, t.TempDir(), FetchOptions{}); err == nil {
		t.Error("unknown requirement accepted")
	}
}

func TestCatalogRequires(t *testing.T) {
	for _, m := range Catalog {
		for _, id := range m.Requires {
			if r, ok := Lookup(id); !ok || len(r.Requires) > 0 {
				t.Errorf("%s requires %s, which is unknown or has requirements of its own", m.ID, id)
			}
		}
		if m.Kind == "tts" && len(m.Requires) == 0 {
			t.Errorf("TTS voice %s does not require the lexicon", m.ID)
		}
	}
}

func TestFetchFile(t *testing.T) {
	body := []byte("ggml weights")
	srv := serve(t, map[string][]byte{"/m.bin": body})
	dir := filepath.Join(t.TempDir(), "models")
	m := Model{ID: "test", Engine: WhisperCpp, File: "m.bin", URL: srv.URL + "/m.bin", SHA256: sum(body), Size: int64(len(body))}

	var recorded []string
	var last int64
	o := FetchOptions{
		Record:   func(p string, isDir bool) error { recorded = append(recorded, p); return nil },
		Progress: func(done, total int64) { last = done },
	}
	p, err := Fetch(context.Background(), m, dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(body) {
		t.Fatalf("file holds %q", got)
	}
	if !m.Installed(dir) {
		t.Error("not installed after fetch")
	}
	if strings.Join(recorded, ",") != dir+","+p {
		t.Errorf("recorded %v", recorded)
	}
	if last != int64(len(body)) {
		t.Errorf("progress ended at %d", last)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".download-*"))
	if len(leftovers) > 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}

	// A second fetch does not download again.
	srv.Close()
	if _, err := Fetch(context.Background(), m, dir, FetchOptions{}); err != nil {
		t.Fatalf("refetch: %v", err)
	}
}

func TestFetchRejectsBadChecksum(t *testing.T) {
	body := []byte("tampered")
	srv := serve(t, map[string][]byte{"/m.bin": body})
	dir := t.TempDir()
	m := Model{ID: "test", File: "m.bin", URL: srv.URL + "/m.bin", SHA256: sum([]byte("original")), Size: int64(len(body))}
	_, err := Fetch(context.Background(), m, dir, FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if m.Installed(dir) {
		t.Error("a file that failed its checksum was installed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}

	m.Size = 3 // wrong size
	m.SHA256 = sum(body)
	if _, err := Fetch(context.Background(), m, dir, FetchOptions{}); err == nil {
		t.Error("wrong size accepted")
	}
}

func TestFetchArchive(t *testing.T) {
	good, err := os.ReadFile("testdata/model.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	evil, err := os.ReadFile("testdata/evil.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, map[string][]byte{"/good.tar.bz2": good, "/evil.tar.bz2": evil})
	dir := t.TempDir()

	m := Model{ID: "arch", Engine: Sherpa, File: "m1", Archive: "tar.bz2", URL: srv.URL + "/good.tar.bz2", SHA256: sum(good), Size: int64(len(good))}
	p, err := Fetch(context.Background(), m, dir, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(p, "sub", "b.txt")); string(b) != "x\n" {
		t.Errorf("sub/b.txt = %q", b)
	}
	if !m.Installed(dir) {
		t.Error("archive model not installed after fetch")
	}

	bad := Model{ID: "evil", File: "m1", Archive: "tar.bz2", URL: srv.URL + "/evil.tar.bz2", SHA256: sum(evil), Size: int64(len(evil))}
	dir2 := t.TempDir()
	if _, err := Fetch(context.Background(), bad, dir2, FetchOptions{}); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir2), "evil.txt")); err == nil {
		t.Fatal("archive wrote outside the models folder")
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Resolve("whisper-base.en", dir, WhisperCpp)
	var missing *MissingError
	if !errors.As(err, &missing) || !strings.Contains(err.Error(), "footman models fetch whisper-base.en") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := Resolve("whisper-base.en", dir, Sherpa); err == nil || errors.As(err, &missing) {
		t.Errorf("engine mismatch: err = %v", err)
	}
	if _, _, err := Resolve("nope", dir, ""); err == nil {
		t.Error("unknown name accepted")
	}
	f := filepath.Join(dir, "custom.bin")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _, err := Resolve(f, dir, WhisperCpp); err != nil || p != f {
		t.Errorf("path: %q %v", p, err)
	}
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Catalog {
		if seen[m.ID] {
			t.Errorf("duplicate %s", m.ID)
		}
		seen[m.ID] = true
		if len(m.SHA256) != 64 || m.Size <= 0 || !strings.HasPrefix(m.URL, "https://") || m.File == "" || m.License == "" {
			t.Errorf("incomplete entry %+v", m)
		}
		if m.Engine != WhisperCpp && m.Engine != Sherpa {
			t.Errorf("%s: engine %q", m.ID, m.Engine)
		}
	}
}
