package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runPersona(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdPersona(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestPersonaCommand(t *testing.T) {
	data := t.TempDir()
	old := personaDataDir
	personaDataDir = func() string { return data }
	t.Cleanup(func() { personaDataDir = old })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	code, out, _ := runPersona(t, "list")
	if code != 0 {
		t.Fatalf("list exit %d: %s", code, out)
	}
	for _, id := range []string{"ashford", "klaus", "pettigrew", "lucy"} {
		if !strings.Contains(out, id) {
			t.Errorf("list lacks %s:\n%s", id, out)
		}
	}

	src := t.TempDir()
	write := func(n, s string) { _ = os.WriteFile(filepath.Join(src, n), []byte(s), 0o644) }
	write("persona.toml", "[persona]\nid=\"mine\"\nname=\"Mine\"\nversion=\"1\"\nlicense=\"CC0-1.0\"\n[wake]\nwords=[\"mine\"]\n[address]\nfallback=\"sir\"\n[voice]\nengine=\"piper\"\nmodel=\"vits-piper-en_GB-alan-medium\"\n")
	write("style.md", "Be brief.")
	write("lines.en.toml", "ack=[\"Ok.\"]")
	if code, _, e := runPersona(t, "install", src); code != 0 {
		t.Fatalf("install: %s", e)
	}
	if code, _, _ := runPersona(t, "install", src); code == 0 {
		t.Error("reinstall without --replace should fail")
	}
	if _, out, _ := runPersona(t, "list"); !strings.Contains(out, "mine") || !strings.Contains(out, "user") {
		t.Errorf("list:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(data, "install-receipt.json")); err != nil || !strings.Contains(string(b), "mine") {
		t.Errorf("receipt: %v %s", err, b)
	}
	if code, _, e := runPersona(t, "remove", "mine"); code != 0 {
		t.Fatalf("remove: %s", e)
	}
	if code, _, _ := runPersona(t, "remove", "ashford"); code == 0 {
		t.Error("removing a shipped pack should fail")
	}
	if code, _, _ := runPersona(t); code != 2 {
		t.Errorf("no args: %d", code)
	}
}
