package install_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
)

var ctx = context.Background()

// env is a temp HOME with a fake platform whose directories live inside it,
// an unrelated file that must survive everything, and an installed binary.
type env struct {
	t      *testing.T
	home   string
	p      *fake.Platform
	binary string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := fake.New(registry.Map{"root": home})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, home: home, p: p, binary: filepath.Join(home, "bin", "footman")}
	if err := os.MkdirAll(filepath.Dir(e.binary), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write(filepath.Join(home, ".config", "other-app", "settings"), "not ours")
	return e
}

func (e *env) write(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) paths() platform.Paths { return e.p.Paths() }

func (e *env) secrets() *fake.Secrets { return e.p.Secrets().(*fake.Secrets) }

func (e *env) service() bool {
	ok, _ := e.p.Service().Installed(ctx)
	return ok
}

func (e *env) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (e *env) hasSecret(name string) bool {
	_, err := e.secrets().Get(ctx, name)
	return err == nil
}

// snapshot lists every path under HOME with a content hash for files.
func (e *env) snapshot() map[string]string {
	e.t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(e.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.home, p)
		if d.IsDir() {
			m[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m[rel] = string(sha256.New().Sum(b))
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func diff(before, after map[string]string) []string {
	var d []string
	for k, v := range after {
		if bv, ok := before[k]; !ok || bv != v {
			d = append(d, "+ "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			d = append(d, "- "+k)
		}
	}
	slices.Sort(d)
	return d
}

func (e *env) setup(o install.SetupOptions) *install.SetupResult {
	e.t.Helper()
	o.Platform = e.p
	if o.Binary == "" {
		o.Binary = e.binary
		e.write(e.binary, "binary") // what scripts/install.sh would have put there
	}
	res, err := install.Setup(ctx, o)
	if err != nil {
		e.t.Fatalf("Setup: %v", err)
	}
	return res
}

// fixture paths a running daemon and its installers would create.
func (e *env) personaDir() string { return filepath.Join(e.paths().Data(), "personas", "alfred") }
func (e *env) modelPath() string {
	return filepath.Join(e.paths().Data(), "models", "whisper-turbo.bin")
}
func (e *env) cacheFile() string { return filepath.Join(e.paths().Cache(), "clips", "hello.wav") }

// daemonInstalls mimics the other components recording what they add.
func (e *env) daemonInstalls() {
	e.t.Helper()
	e.write(filepath.Join(e.personaDir(), "voice.bin"), strings.Repeat("v", 3000))
	e.write(e.modelPath(), strings.Repeat("m", 5000))
	e.write(e.cacheFile(), "riff")
	err := install.Record(e.p,
		install.Entry{Kind: install.KindDir, Group: install.GroupPersonas, Path: filepath.Dir(e.personaDir())},
		install.Entry{Kind: install.KindTree, Group: install.GroupPersonas, Path: e.personaDir(), Name: "alfred"},
		install.Entry{Kind: install.KindDir, Group: install.GroupModels, Path: filepath.Dir(e.modelPath())},
		install.Entry{Kind: install.KindModel, Group: install.GroupModels, Path: e.modelPath(), Name: "whisper-turbo"},
		install.Entry{Kind: install.KindDir, Group: install.GroupCache, Path: e.paths().Cache()},
		install.Entry{Kind: install.KindDir, Group: install.GroupCache, Path: filepath.Dir(e.cacheFile())},
		install.Entry{Kind: install.KindFile, Group: install.GroupCache, Path: e.cacheFile()},
	)
	if err != nil {
		e.t.Fatal(err)
	}
}

func fullSetup() install.SetupOptions {
	return install.SetupOptions{Name: "Ian", Persona: "alfred", Provider: "claude", APIKey: "sk-secret-123", Hotkey: "F9"}
}

func TestSetupRecordsEverything(t *testing.T) {
	e := newEnv(t)
	res := e.setup(fullSetup())

	cfg, err := config.Load(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"user.name": "Ian", "personas.default": "alfred", "ai.provider": "claude", "input.dictate.key": "F9",
	} {
		if got := cfg.String(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if ref, ok := cfg.Secret("ai.providers.claude.api_key"); !ok || ref.Name != "claude" {
		t.Errorf("api_key ref = %v, %v", ref, ok)
	}
	if got, err := e.secrets().Get(ctx, "claude"); err != nil || got != "sk-secret-123" {
		t.Errorf("keychain = %q, %v", got, err)
	}
	if !e.service() {
		t.Error("service not installed")
	}
	if !e.exists(filepath.Join(e.paths().Data(), install.DatabaseFile)) {
		t.Error("database not created")
	}

	// The key must be in the keychain only.
	for _, f := range []string{res.ConfigPath, res.ReceiptPath} {
		if b, _ := os.ReadFile(f); bytes.Contains(b, []byte("sk-secret-123")) {
			t.Errorf("%s contains the API key", f)
		}
	}

	rec, err := install.LoadReceipt(res.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []install.Entry{
		{Kind: install.KindDir, Group: install.GroupConfig, Path: e.paths().Config()},
		{Kind: install.KindDir, Group: install.GroupData, Path: e.paths().Data()},
		{Kind: install.KindFile, Group: install.GroupProgram, Path: e.binary},
		{Kind: install.KindSecret, Group: install.GroupSecrets, Name: "claude"},
		{Kind: install.KindFile, Group: install.GroupConfig, Path: res.ConfigPath},
		{Kind: install.KindFile, Group: install.GroupData, Path: filepath.Join(e.paths().Data(), install.DatabaseFile)},
		{Kind: install.KindService, Group: install.GroupProgram, Name: install.ServiceName},
	}
	for _, w := range want {
		if !slices.Contains(rec.Entries, w) {
			t.Errorf("receipt missing %+v", w)
		}
	}
	if len(rec.Entries) != len(want) {
		t.Errorf("receipt has %d entries, want %d: %+v", len(rec.Entries), len(want), rec.Entries)
	}
}

func TestSetupDefaultsAndNoService(t *testing.T) {
	e := newEnv(t)
	res := e.setup(install.SetupOptions{NoService: true})
	cfg, err := config.Load(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("input.dictate.key"); got != "F8" {
		t.Errorf("default hotkey = %q, want F8 (ADR-0022)", got)
	}
	if e.service() {
		t.Error("service installed despite NoService")
	}
	rec, _ := install.LoadReceipt(res.ReceiptPath)
	if rec.Has(install.KindService, install.GroupProgram) || rec.Has(install.KindSecret, install.GroupSecrets) {
		t.Errorf("receipt records a service or secret that was not installed: %+v", rec.Entries)
	}
}

func TestSetupRerunKeepsUserEditsAndDoesNotDuplicate(t *testing.T) {
	e := newEnv(t)
	res := e.setup(fullSetup())
	// The user edits the file by hand.
	b, _ := os.ReadFile(res.ConfigPath)
	edited := string(b) + "\n# my note\n[history]\ntranscripts = false\n"
	e.write(res.ConfigPath, edited)
	before, _ := install.LoadReceipt(res.ReceiptPath)

	e.setup(install.SetupOptions{Name: "Ian R"})

	after, _ := install.LoadReceipt(res.ReceiptPath)
	if len(after.Entries) != len(before.Entries) {
		t.Errorf("rerun changed the receipt: %d -> %d entries", len(before.Entries), len(after.Entries))
	}
	got, _ := os.ReadFile(res.ConfigPath)
	if !strings.Contains(string(got), "# my note") || !strings.Contains(string(got), "transcripts = false") {
		t.Errorf("rerun lost the user's edits:\n%s", got)
	}
	cfg, err := config.Load(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.String("user.name") != "Ian R" || cfg.String("input.dictate.key") != "F9" {
		t.Errorf("name=%q hotkey=%q", cfg.String("user.name"), cfg.String("input.dictate.key"))
	}
}

func TestSetupRejectsBadProvider(t *testing.T) {
	e := newEnv(t)
	_, err := install.Setup(ctx, install.SetupOptions{Platform: e.p, Provider: "Evil\"\n[x]"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if e.exists(e.paths().Config()) {
		t.Error("setup touched disk before validating")
	}
}

func TestUninstallPurgeLeavesNothingBehind(t *testing.T) {
	e := newEnv(t)
	before := e.snapshot()

	e.setup(fullSetup())
	e.daemonInstalls()
	if len(diff(before, e.snapshot())) == 0 {
		t.Fatal("setup changed nothing")
	}

	rep, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	if d := diff(before, e.snapshot()); len(d) != 0 {
		t.Errorf("tree differs after purge:\n%s", strings.Join(d, "\n"))
	}
	if e.service() {
		t.Error("service still installed")
	}
	if e.hasSecret("claude") {
		t.Error("secret still in keychain after purge")
	}
	if len(rep.Kept) != 0 || len(rep.Left) != 0 {
		t.Errorf("kept=%v left=%v", rep.Kept, rep.Left)
	}
	// The unrelated file and the binary's folder are untouched.
	if !e.exists(filepath.Join(e.home, ".config", "other-app", "settings")) || !e.exists(filepath.Dir(e.binary)) {
		t.Error("removed something that was not Footman's")
	}
}

// TestKeepOptions checks that each option keeps exactly what it says.
func TestKeepOptions(t *testing.T) {
	type item struct {
		name string
		path func(*env) string
	}
	items := []item{
		{"binary", func(e *env) string { return e.binary }},
		{"config", func(e *env) string { return filepath.Join(e.paths().Config(), "config.toml") }},
		{"database", func(e *env) string { return filepath.Join(e.paths().Data(), install.DatabaseFile) }},
		{"persona", func(e *env) string { return e.personaDir() }},
		{"model", func(e *env) string { return e.modelPath() }},
		{"cache", func(e *env) string { return e.cacheFile() }},
		{"receipt", func(e *env) string { return install.ReceiptPath(e.paths()) }},
	}
	tests := []struct {
		name string
		opts install.UninstallOptions
		kept []string // items that survive; everything else is gone
		keys bool     // keychain entry survives
	}{
		{"remove everything, keys kept", install.UninstallOptions{}, nil, true},
		{"remove everything incl. keys", install.UninstallOptions{RemoveSecrets: true}, nil, false},
		{"keep config", install.UninstallOptions{KeepConfig: true}, []string{"config"}, true},
		{"keep data", install.UninstallOptions{KeepData: true}, []string{"database", "persona", "receipt"}, true},
		{"keep config and data", install.UninstallOptions{KeepConfig: true, KeepData: true},
			[]string{"config", "database", "persona", "receipt"}, true},
		{"keep config and data, drop keys", install.UninstallOptions{KeepConfig: true, KeepData: true, RemoveSecrets: true},
			[]string{"config", "database", "persona", "receipt"}, false},
		{"purge", install.UninstallOptions{Purge: true}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.setup(fullSetup())
			e.daemonInstalls()
			tt.opts.Platform = e.p
			if _, err := install.Uninstall(ctx, tt.opts); err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				want := slices.Contains(tt.kept, it.name)
				if got := e.exists(it.path(e)); got != want {
					t.Errorf("%s exists=%v, want %v", it.name, got, want)
				}
			}
			if got := e.hasSecret("claude"); got != tt.keys {
				t.Errorf("keychain entry exists=%v, want %v", got, tt.keys)
			}
			if e.service() {
				t.Error("service always goes, but is still installed")
			}
			if !e.exists(filepath.Join(e.home, ".config", "other-app", "settings")) {
				t.Error("unrelated file removed")
			}
			// Only kept things (and the folders holding them) may remain.
			if len(tt.kept) == 0 {
				for _, d := range []string{e.paths().Config(), e.paths().Data(), e.paths().Cache()} {
					if e.exists(d) {
						t.Errorf("%s left behind", d)
					}
				}
			}
		})
	}
}

func TestKeepDataThenPurgeFinishesTheJob(t *testing.T) {
	e := newEnv(t)
	before := e.snapshot()
	e.setup(fullSetup())
	e.daemonInstalls()

	if _, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, KeepConfig: true, KeepData: true}); err != nil {
		t.Fatal(err)
	}
	// The receipt was rewritten to describe only what is left.
	rec, err := install.LoadReceipt(install.ReceiptPath(e.paths()))
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range rec.Entries {
		if en.Group == install.GroupProgram || en.Group == install.GroupModels || en.Group == install.GroupCache {
			t.Errorf("receipt still lists removed %+v", en)
		}
	}
	if _, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true}); err != nil {
		t.Fatal(err)
	}
	if d := diff(before, e.snapshot()); len(d) != 0 {
		t.Errorf("tree differs after the second uninstall:\n%s", strings.Join(d, "\n"))
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.setup(fullSetup())
	e.daemonInstalls()
	before := e.snapshot()

	for _, o := range []install.UninstallOptions{
		{Purge: true, DryRun: true},
		{KeepConfig: true, DryRun: true},
		{RemoveSecrets: true, DryRun: true},
	} {
		o.Platform = e.p
		rep, err := install.Uninstall(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if d := diff(before, e.snapshot()); len(d) != 0 {
			t.Errorf("dry run %+v changed the tree:\n%s", o, strings.Join(d, "\n"))
		}
		if !e.service() || !e.hasSecret("claude") {
			t.Error("dry run touched the service or keychain")
		}
		if !rep.DryRun || len(rep.Removed) == 0 {
			t.Errorf("report = %+v", rep)
		}
		var out bytes.Buffer
		rep.Write(&out)
		if !strings.Contains(out.String(), "Dry run") || !strings.Contains(out.String(), "Would remove") {
			t.Errorf("summary:\n%s", out.String())
		}
	}
}

func TestUninstallLeavesFilesItDidNotInstall(t *testing.T) {
	e := newEnv(t)
	e.setup(fullSetup())
	stray := filepath.Join(e.paths().Config(), "my-notes.txt")
	e.write(stray, "mine")

	rep, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(stray); string(b) != "mine" {
		t.Error("stray file was removed")
	}
	if !slices.Contains(rep.Left, e.paths().Config()) {
		t.Errorf("Left = %v", rep.Left)
	}
	if e.exists(filepath.Join(e.paths().Config(), "config.toml")) {
		t.Error("config.toml should have been removed")
	}
}

func TestUninstallRemovesDatabaseCompanions(t *testing.T) {
	e := newEnv(t)
	e.setup(fullSetup())
	db := filepath.Join(e.paths().Data(), install.DatabaseFile)
	e.write(db+"-wal", "w")
	e.write(db+"-shm", "s")
	if _, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true}); err != nil {
		t.Fatal(err)
	}
	if e.exists(e.paths().Data()) {
		t.Error("data dir left behind")
	}
}

func TestUninstallWithoutReceipt(t *testing.T) {
	e := newEnv(t)
	before := e.snapshot()
	_, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true})
	if !errors.Is(err, install.ErrNoReceipt) {
		t.Fatalf("err = %v, want ErrNoReceipt", err)
	}
	if d := diff(before, e.snapshot()); len(d) != 0 {
		t.Errorf("changed the tree: %v", d)
	}
}

func TestPurgeConflictsWithKeep(t *testing.T) {
	e := newEnv(t)
	e.setup(fullSetup())
	_, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true, KeepData: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !e.service() {
		t.Error("acted despite the conflicting options")
	}
}

func TestReceiptRefusesUnsafeEntries(t *testing.T) {
	e := newEnv(t)
	for name, entry := range map[string]string{
		"home":     `{"kind":"tree","group":"data","path":"` + e.home + `"}`,
		"above":    `{"kind":"tree","group":"data","path":"` + filepath.Dir(e.home) + `"}`,
		"root":     `{"kind":"tree","group":"data","path":"/"}`,
		"relative": `{"kind":"file","group":"data","path":"data/x"}`,
		"dotdot":   `{"kind":"file","group":"data","path":"` + e.home + `/a/../b"}`,
		"kind":     `{"kind":"shell","group":"data","path":"` + e.home + `/x"}`,
		"group":    `{"kind":"file","group":"everything","path":"` + e.home + `/x"}`,
		"noname":   `{"kind":"secret","group":"secrets"}`,
	} {
		path := install.ReceiptPath(e.paths())
		e.write(path, `{"version":1,"entries":[`+entry+`]}`)
		if _, err := install.LoadReceipt(path); err == nil {
			t.Errorf("%s: unsafe entry accepted", name)
		}
	}
	if !e.exists(e.home) {
		t.Fatal("unreachable")
	}
}

func TestPlanListing(t *testing.T) {
	e := newEnv(t)
	e.setup(fullSetup())
	e.daemonInstalls()
	pl, err := install.LoadPlan(e.p)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	pl.Listing(&out)
	s := out.String()
	for _, want := range []string{"Program", "footman", "service unit", "Models", "whisper-turbo", "Config", "config.toml",
		"Data", "footman.db", "Personas", "alfred", "Secrets", "1 API key in the OS keychain", "Cache", "hello.wav"} {
		if !strings.Contains(s, want) {
			t.Errorf("listing lacks %q:\n%s", want, s)
		}
	}
	for _, pg := range pl.Groups {
		switch pg.Group {
		case install.GroupModels:
			if pg.Size != 5000 {
				t.Errorf("models size = %d", pg.Size)
			}
		case install.GroupPersonas:
			if pg.Size != 3000 {
				t.Errorf("personas size = %d", pg.Size)
			}
		}
	}
}

func TestPrompter(t *testing.T) {
	t.Run("setup", func(t *testing.T) {
		var out bytes.Buffer
		p := install.NewPrompter(strings.NewReader("Ian\n\nopenai\nF9\n"), &out, func() (string, error) { return "sk-x", nil })
		o := install.SetupOptions{}
		if err := p.AskSetup(&o); err != nil {
			t.Fatal(err)
		}
		want := install.SetupOptions{Name: "Ian", Persona: "ashford", Provider: "openai", APIKey: "sk-x", Hotkey: "F9"}
		if !reflect.DeepEqual(o, want) { // SetupOptions has func fields
			t.Errorf("got %+v, want %+v", o, want)
		}
		if strings.Contains(out.String(), "sk-x") {
			t.Error("secret echoed")
		}
	})
	t.Run("setup skips answered and ollama key", func(t *testing.T) {
		p := install.NewPrompter(strings.NewReader(""), &bytes.Buffer{}, func() (string, error) { t.Error("asked for a key"); return "", nil })
		o := install.SetupOptions{Name: "A", Persona: "b", Provider: "ollama", Hotkey: "F8"}
		if err := p.AskSetup(&o); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("uninstall menu", func(t *testing.T) {
		e := newEnv(t)
		e.setup(fullSetup())
		pl, _ := install.LoadPlan(e.p)
		cases := []struct {
			in      string
			want    install.UninstallOptions
			proceed bool
		}{
			{"\n\ny\n", install.UninstallOptions{KeepConfig: true, KeepData: true}, true},    // default 2, keys not removed
			{"1\ny\ny\n", install.UninstallOptions{RemoveSecrets: true}, true},               // 1, confirm keys, go
			{"3\nn\ny\n", install.UninstallOptions{KeepConfig: true}, true},                  // 3, keep keys
			{"2\nn\nn\n", install.UninstallOptions{KeepConfig: true, KeepData: true}, false}, // declined
			{"", install.UninstallOptions{KeepConfig: true, KeepData: true}, false},          // EOF declines
		}
		for _, c := range cases {
			var o install.UninstallOptions
			ok, err := install.NewPrompter(strings.NewReader(c.in), &bytes.Buffer{}, nil).AskUninstall(pl, &o, false)
			if err != nil {
				t.Fatal(err)
			}
			if o != c.want || ok != c.proceed {
				t.Errorf("input %q: got %+v proceed=%v, want %+v proceed=%v", c.in, o, ok, c.want, c.proceed)
			}
		}
	})
	t.Run("uninstall with flags does not ask the menu", func(t *testing.T) {
		e := newEnv(t)
		e.setup(fullSetup())
		pl, _ := install.LoadPlan(e.p)
		o := install.UninstallOptions{KeepData: true}
		var out bytes.Buffer
		ok, err := install.NewPrompter(strings.NewReader("n\ny\n"), &out, nil).AskUninstall(pl, &o, true)
		if err != nil || !ok || o.RemoveSecrets || strings.Contains(out.String(), "What should be kept") {
			t.Errorf("ok=%v err=%v o=%+v\n%s", ok, err, o, out.String())
		}
	})
}

// speechStep stands in for the speech part of setup: it downloads a "model"
// and a cache folder, recording them as it goes.
func (e *env) speechStep(sawService *bool) func(context.Context, install.SpeechEnv) error {
	return func(_ context.Context, se install.SpeechEnv) error {
		*sawService = e.service()
		models := filepath.Join(se.DataDir, "models")
		model := filepath.Join(models, "m.bin")
		if err := se.Record(install.Entry{Kind: install.KindDir, Group: install.GroupModels, Path: models}); err != nil {
			return err
		}
		if err := se.Record(install.Entry{Kind: install.KindModel, Group: install.GroupModels, Path: model, Name: "m"}); err != nil {
			return err
		}
		e.write(model, "weights")
		if err := se.Record(install.Entry{Kind: install.KindDir, Group: install.GroupCache, Path: se.CacheDir}); err != nil {
			return err
		}
		cache := filepath.Join(se.CacheDir, "vulkan")
		if err := se.Record(install.Entry{Kind: install.KindTree, Group: install.GroupCache, Path: cache}); err != nil {
			return err
		}
		e.write(filepath.Join(cache, "shader"), "compiled")
		se.Step("speech       fetched m")
		return nil
	}
}

func TestSetupSpeechStepRunsBeforeTheServiceAndUninstallRemovesItsFiles(t *testing.T) {
	e := newEnv(t)
	before := e.snapshot()
	var sawService bool
	var out bytes.Buffer
	o := fullSetup()
	o.Speech = e.speechStep(&sawService)
	o.Out = &out
	res := e.setup(o)
	if sawService {
		t.Error("the service was installed before the models were fetched")
	}
	if !e.service() {
		t.Error("service not installed after the speech step")
	}
	if !strings.Contains(out.String(), "speech       fetched m\n") || !strings.Contains(strings.Join(res.Lines, "\n"), "fetched m") {
		t.Errorf("progress not streamed: out=%q lines=%v", out.String(), res.Lines)
	}
	if _, err := install.Uninstall(ctx, install.UninstallOptions{Platform: e.p, Purge: true}); err != nil {
		t.Fatal(err)
	}
	if d := diff(before, e.snapshot()); len(d) != 0 {
		t.Errorf("tree differs after purge:\n%s", strings.Join(d, "\n"))
	}
}

func TestSetupSkipModels(t *testing.T) {
	e := newEnv(t)
	o := fullSetup()
	o.SkipModels = true
	o.Speech = func(context.Context, install.SpeechEnv) error {
		t.Error("speech step ran with SkipModels")
		return nil
	}
	res := e.setup(o)
	if !strings.Contains(strings.Join(res.Lines, "\n"), "--skip-models") || !e.service() {
		t.Errorf("lines %v service %v", res.Lines, e.service())
	}
}

func TestSetupSpeechFailureKeepsTheReceiptAndSkipsTheService(t *testing.T) {
	e := newEnv(t)
	o := fullSetup()
	o.Platform = e.p
	o.Speech = func(_ context.Context, se install.SpeechEnv) error {
		if err := se.Record(install.Entry{Kind: install.KindDir, Group: install.GroupModels, Path: filepath.Join(se.DataDir, "models")}); err != nil {
			return err
		}
		return errors.New("no route to host")
	}
	_, err := install.Setup(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("err = %v", err)
	}
	if e.service() {
		t.Error("service installed although models are missing")
	}
	if _, err := install.LoadReceipt(install.ReceiptPath(e.paths())); err != nil {
		t.Errorf("receipt: %v", err)
	}
}
