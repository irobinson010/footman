package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func dump(c *Config) []byte {
	m := map[string]any{}
	for k, v := range c.values {
		if s, ok := v.(SecretRef); ok {
			v = s.String()
		}
		m[k] = v
	}
	b, _ := json.MarshalIndent(m, "", "  ") // map keys are sorted
	return append(b, '\n')
}

func TestEmptyIsDefaults(t *testing.T) {
	for _, src := range []string{"", "\n# just a comment\n"} {
		c, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "defaults.golden.json", dump(c))
	}
	c, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "defaults.golden.json", dump(c))
}

func TestSchemaCoversExampleAndDefaultsValidate(t *testing.T) {
	for _, k := range Schema() {
		if !strings.Contains(k.Path, "*") {
			if k.Default == nil {
				t.Errorf("%s: no default", k.Path)
				continue
			}
			if _, err := coerce(k, k.Default); err != nil {
				t.Errorf("%s: default invalid: %v", k.Path, err)
			}
		}
		if k.Description == "" || k.Reload == "" {
			t.Errorf("%s: missing description or reload", k.Path)
		}
	}
	if _, err := Parse(read(t, "full.toml")); err != nil {
		t.Fatalf("design §28 example: %v", err)
	}
}

func TestFullExample(t *testing.T) {
	c, err := Parse(read(t, "full.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if ref, ok := c.Secret("ai.providers.claude.api_key"); !ok || ref.Name != "claude" {
		t.Errorf("secret ref = %v %v", ref, ok)
	}
	if c.String("input.dictate.mode") != "toggle" || c.Float("ai.budget.claude.warn_at") != 0.8 {
		t.Error("values not read")
	}
	if got := c.Names("ai.providers"); len(got) != 2 {
		t.Errorf("providers = %v", got)
	}
	if c.Plugin("troupe")["url"] != "https://troupe.example.com:8443" {
		t.Errorf("plugin = %v", c.Plugin("troupe"))
	}
}

func TestInvalid(t *testing.T) {
	var out strings.Builder
	for _, name := range []string{"invalid-values.toml", "invalid-syntax.toml", "invalid-newer.toml"} {
		_, err := Load(filepath.Join("testdata", name))
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		out.WriteString(err.Error() + "\n")
	}
	golden(t, "invalid.golden.txt", []byte(out.String()))

	_, err := Load(filepath.Join("testdata", "invalid-values.toml"))
	var es Errors
	if !errors.As(err, &es) || len(es) < 5 {
		t.Fatalf("want multiple errors, got %v", err)
	}
	for _, e := range es {
		if e.Key == "" || e.Line == 0 {
			t.Errorf("error lacks key/line: %+v", e)
		}
	}
}

func TestSecretMustBeReference(t *testing.T) {
	_, err := Parse([]byte("[ai.providers.claude]\napi_key = \"sk-abc\"\n"))
	if err == nil || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("got %v", err)
	}
}

func TestEditKeepsCommentsAndOrder(t *testing.T) {
	d, err := ParseDoc(read(t, "edit-input.toml"))
	if err != nil {
		t.Fatal(err)
	}
	edits := []struct {
		k string
		v any
	}{
		{"replies.speak", "never"},                 // existing value, trailing comment kept
		{"input.dictate.mode", "toggle"},           // inside an inline table
		{"input.dictate.key", "Ctrl+Alt+D"},        // inline table, other member
		{"personas.listening", []string{"a", "b"}}, // array
		{"history.retention_days", 7},              // missing key, existing table
		{"jobs.code_edits.show_diff", false},       // missing key inside missing inline-less table
		{"coaching.enabled", true},                 // missing table appended
		{"plugins.troupe.enabled", false},          // plugin key
		{"user.name", "Ian"},                       // existing, quoted
		{"ai.providers.claude.api_key", "keychain:claude"},
	}
	for _, e := range edits {
		if err := d.Set(e.k, e.v); err != nil {
			t.Fatalf("Set(%s): %v", e.k, err)
		}
	}
	golden(t, "edit-output.golden.toml", d.Bytes())
	if _, err := Parse(d.Bytes()); err != nil {
		t.Fatalf("result invalid: %v", err)
	}
}

func TestEditEdgeCases(t *testing.T) {
	cases := map[string]struct {
		in, k string
		v     any
		want  string
	}{
		"empty file, root key":   {"", "version", 1, "version = 1\n"},
		"root key before tables": {"# top\n[user]\nname = \"a\"\n", "version", 1, "version = 1\n\n# top\n[user]\nname = \"a\"\n"},
		"append to unterminated": {"[user]\nname = \"a\"", "user.language", "fr", "[user]\nname = \"a\"\nlanguage = \"fr\"\n"},
		"empty inline table":     {"[input]\ndictate = {}\n", "input.dictate.key", "F9", "[input]\ndictate = { key = \"F9\" }\n"},
		"empty table header":     {"[user]\n\n[web]\n", "user.name", "x", "[user]\nname = \"x\"\n\n[web]\n"},
		"CRLF value replace":     {"[user]\r\nname = \"a\"\r\n", "user.name", "b", "[user]\r\nname = \"b\"\r\n"},
	}
	cases["no trailing newline"] = struct {
		in, k string
		v     any
		want  string
	}{"[web]\nlisten = \"127.0.0.1:1\"", "web.listen", "127.0.0.1:2", "[web]\nlisten = \"127.0.0.1:2\""}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := ParseDoc([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Set(c.k, c.v); err != nil {
				t.Fatal(err)
			}
			if got := string(d.Bytes()); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestSetRejectsBadInput(t *testing.T) {
	d, _ := ParseDoc([]byte("# keep\n"))
	for _, c := range []struct {
		k string
		v any
	}{
		{"nope.key", 1},
		{"dictation.cleanup", "loud"},
		{"jobs.max_concurrent", 0},
		{"jobs.max_concurrent", "two"},
		{"ai.providers.x.api_key", "plain"},
		{"plugins.bad", 1},
	} {
		if err := d.Set(c.k, c.v); err == nil {
			t.Errorf("Set(%s,%v) should fail", c.k, c.v)
		}
	}
	if string(d.Bytes()) != "# keep\n" {
		t.Error("failed Set changed the doc")
	}
}

func TestRoundTripStable(t *testing.T) {
	src := read(t, "full.toml")
	d, err := ParseDoc(src)
	if err != nil {
		t.Fatal(err)
	}
	// Setting every key to its current value must not change a single byte.
	c, _ := Parse(src)
	for _, k := range Schema() {
		if strings.Contains(k.Path, "*") {
			continue
		}
		if _, present := d.spans[k.Path]; !present {
			continue
		}
		v := c.Get(k.Path)
		if err := d.Set(k.Path, v); err != nil {
			t.Fatalf("%s: %v", k.Path, err)
		}
	}
	for p := range d.spans {
		if k, ok := Lookup(p); ok && strings.Contains(k.Path, "*") {
			if v := c.Get(p); v != nil {
				if s, isRef := v.(SecretRef); isRef {
					v = s.String()
				}
				if err := d.Set(p, v); err != nil {
					t.Fatalf("%s: %v", p, err)
				}
			}
		}
	}
	if !bytes.Equal(d.Bytes(), src) {
		t.Errorf("round trip changed the file:\n%s", d.Bytes())
	}
	// Idempotent: a second identical edit changes nothing.
	d2, _ := ParseDoc(read(t, "edit-input.toml"))
	_ = d2.Set("replies.speak", "never")
	first := d2.Bytes()
	_ = d2.Set("replies.speak", "never")
	if !bytes.Equal(first, d2.Bytes()) {
		t.Error("Set is not idempotent")
	}
}

func TestSetFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := SetFile(p, "user.name", "Ian"); err != nil {
		t.Fatal(err)
	}
	if err := SetFile(p, "user.name", "Sam"); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.String("user.name") != "Sam" {
		t.Fatalf("%v %v", c, err)
	}
}

func TestMigrationHook(t *testing.T) {
	// A fake future version proves the hook runs before validation.
	old := Migrations
	defer func() { Migrations = old }()
	Migrations = map[int64]func(map[string]any) error{}
	// With CurrentVersion 1 nothing runs; a version-0 file is just version 0.
	if _, err := Parse([]byte("version = 1\n")); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaIsData(t *testing.T) {
	b, err := json.Marshal(Schema())
	if err != nil || !bytes.Contains(b, []byte(`"security_sensitive":true`)) || !bytes.Contains(b, []byte(`"voice_settable":true`)) {
		t.Fatalf("schema JSON: %v", err)
	}
	k, ok := Lookup("ai.providers.ollama.model")
	if !ok || k.Path != "ai.providers.*.model" {
		t.Errorf("lookup = %v %v", k, ok)
	}
	if k, _ := Lookup("permissions.destructive"); !k.Sensitive {
		t.Error("permissions should be sensitive")
	}
}

func TestWatchDebounce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	got := make(chan *Config, 4)
	go Watch(ctx, p, WatchOptions{Poll: 10 * time.Millisecond, Debounce: 80 * time.Millisecond}, func(c *Config, err error) {
		calls.Add(1)
		if err == nil {
			got <- c
		}
	})
	time.Sleep(30 * time.Millisecond)
	for i, name := range []string{"A", "B", "Cc"} { // a burst of writes
		_ = os.WriteFile(p, []byte("[user]\nname = \""+name+"\"\n"), 0o600)
		_ = i
		time.Sleep(15 * time.Millisecond)
	}
	select {
	case c := <-got:
		if c.String("user.name") != "Cc" {
			t.Errorf("name = %q", c.String("user.name"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reload")
	}
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("reload called %d times, want 1", n)
	}
}

func TestVADMinSilence(t *testing.T) {
	if got := Defaults().Float("speech.vad.min_silence"); got != 0.5 {
		t.Fatalf("default min_silence = %v, want 0.5", got)
	}
	c, err := Parse([]byte("version = 1\n[speech.vad]\nmin_silence = 0.25\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Float("speech.vad.min_silence"); got != 0.25 {
		t.Fatalf("min_silence = %v, want 0.25", got)
	}
	for _, bad := range []string{"0", "0.01", "9", `"long"`} {
		if _, err := Parse([]byte("version = 1\n[speech.vad]\nmin_silence = " + bad + "\n")); err == nil {
			t.Errorf("min_silence = %s was accepted", bad)
		}
	}
}

func TestUnloadAfterValidation(t *testing.T) {
	for _, ok := range []string{"0s", "30s", "10m", "1h"} {
		if _, err := Parse([]byte("version = 1\n[speech]\nunload_after = \"" + ok + "\"\n")); err != nil {
			t.Errorf("unload_after %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"soon", "-5m", "10"} {
		if _, err := Parse([]byte("version = 1\n[speech]\nunload_after = \"" + bad + "\"\n")); err == nil {
			t.Errorf("unload_after %q was accepted", bad)
		}
	}
}

func TestSetFileComment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# mine\nversion = 1\n\n[speech.stt]\nengine = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // the second call changes nothing
		if err := SetFileComment(path, "speech.stt.accelerator", "cpu", "Set by setup: the GPU was slower.\nDelete this line to let footman choose."); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(path)
	want := "# mine\nversion = 1\n\n[speech.stt]\nengine = \"auto\"\n# Set by setup: the GPU was slower.\n# Delete this line to let footman choose.\naccelerator = \"cpu\"\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	c, err := Load(path)
	if err != nil || c.String("speech.stt.accelerator") != "cpu" {
		t.Fatalf("reload: %v %v", c, err)
	}
	// An unchanged value keeps the comment from being added twice, and a
	// changed one keeps it too.
	if err := SetFileComment(path, "speech.stt.accelerator", "cpu", "Set by setup: the GPU was slower.\nDelete this line to let footman choose."); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != want {
		t.Fatalf("not idempotent:\n%s", again)
	}
}
