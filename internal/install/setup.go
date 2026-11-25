package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/store"
)

// Setup defaults.
const (
	// DefaultHotkey is the suggested push-to-talk key (ADR-0022).
	DefaultHotkey = "F8"
	// ServiceName is the name recorded for the background service.
	ServiceName = "footman.service"
	// DatabaseFile is the state database's file name in the data directory.
	DatabaseFile = "footman.db"
)

// Providers are the AI providers offered at setup (ADR-0004).
var Providers = []string{"claude", "openai", "gemini", "ollama"}

var providerName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// SetupOptions says what setup should do. Empty strings mean "leave as is".
type SetupOptions struct {
	// Platform supplies directories, the keychain and the service.
	Platform platform.Platform
	// Binary is the installed footman program, recorded so uninstall removes
	// it. Empty records nothing.
	Binary string
	// Name is what the assistant calls the user (user.name).
	Name string
	// Persona is the default persona (personas.default).
	Persona string
	// Provider is the AI provider (ai.provider).
	Provider string
	// APIKey is stored in the keychain under the provider's name and
	// referenced from the config as keychain:<provider>. It is never written
	// to the config or the receipt.
	APIKey string
	// Hotkey is the push-to-talk key (input.dictate.key). Empty means F8 for
	// a new config and leaves an existing one alone.
	Hotkey string
	// NoService skips installing the background service.
	NoService bool
	// SkipModels skips the speech step: no model downloads, warm-up or
	// benchmark.
	SkipModels bool
	// Speech does the speech part of setup (ADR-0028): it fetches models,
	// warms up and benchmarks. It runs after the config and database exist
	// and before the service is installed, so the service never starts
	// without its models. Nil, or SkipModels, skips it.
	Speech func(ctx context.Context, env SpeechEnv) error
	// Out, if set, receives each step's line as it happens, so a long step
	// shows progress. The lines are also returned in SetupResult.
	Out io.Writer
}

// SpeechEnv is what the speech step of setup gets.
type SpeechEnv struct {
	// ConfigPath is the config file, which exists by now.
	ConfigPath string
	// DataDir and CacheDir are the data and cache folders.
	DataDir, CacheDir string
	// Record adds an entry to the install receipt and saves the receipt
	// at once, so a download that is interrupted is still undone by
	// uninstall (ADR-0023).
	Record func(Entry) error
	// Step reports a line of progress.
	Step func(format string, a ...any)
}

// SetupResult reports what setup did.
type SetupResult struct {
	// ConfigPath is the config file.
	ConfigPath string
	// ReceiptPath is the install receipt.
	ReceiptPath string
	// Lines describes each step, for the user.
	Lines []string
}

const starterConfig = `# Footman settings. Edit freely: your comments are kept.
# API keys are never stored here; they live in the OS keychain (keychain:<name>).
version = 1
`

// Setup creates the folders, the config, the database, the keychain entry and
// the service, recording each in the install receipt as it goes. It is safe to
// run again: it adds to the existing receipt and only changes what the options
// name. If a step fails, the receipt still lists what was done so uninstall can
// undo it.
func Setup(ctx context.Context, o SetupOptions) (res *SetupResult, err error) {
	if o.Platform == nil {
		return nil, errors.New("install: setup needs a platform")
	}
	if o.Provider != "" && !providerName.MatchString(o.Provider) {
		return nil, fmt.Errorf("install: provider %q must be lower-case letters, digits, - or _", o.Provider)
	}
	if o.APIKey != "" && o.Provider == "" {
		o.Provider = Providers[0]
	}
	paths := o.Platform.Paths()
	res = &SetupResult{
		ConfigPath:  filepath.Join(paths.Config(), "config.toml"),
		ReceiptPath: ReceiptPath(paths),
	}
	step := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		res.Lines = append(res.Lines, line)
		if o.Out != nil {
			_, _ = fmt.Fprintln(o.Out, line)
		}
	}

	rec, err := LoadReceipt(res.ReceiptPath)
	if errors.Is(err, ErrNoReceipt) {
		rec = NewReceipt()
	} else if err != nil {
		return nil, err
	}
	defer func() {
		// Whatever got done is on the receipt, even when a later step failed.
		if serr := rec.Save(res.ReceiptPath); serr != nil {
			err = errors.Join(err, serr)
		}
	}()

	for _, d := range []struct {
		path  string
		group Group
	}{{paths.Config(), GroupConfig}, {paths.Data(), GroupData}} {
		if err := os.MkdirAll(d.path, 0o700); err != nil {
			return res, err
		}
		rec.Add(Entry{Kind: KindDir, Group: d.group, Path: d.path})
	}

	if o.Binary != "" {
		rec.Add(Entry{Kind: KindFile, Group: GroupProgram, Path: o.Binary})
		step("program      %s", o.Binary)
	}

	// Keychain first: a config that points at a missing secret is worse than
	// no config.
	if o.APIKey != "" {
		if err := o.Platform.Secrets().Set(ctx, o.Provider, o.APIKey); err != nil {
			return res, fmt.Errorf("store API key in the keychain: %w", err)
		}
		rec.Add(Entry{Kind: KindSecret, Group: GroupSecrets, Name: o.Provider})
		step("keychain     API key stored as %q", o.Provider)
	}

	created := false
	if _, err := os.Stat(res.ConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(res.ConfigPath, []byte(starterConfig), 0o600); err != nil {
			return res, err
		}
		created = true
	} else if err != nil {
		return res, err
	}
	rec.Add(Entry{Kind: KindFile, Group: GroupConfig, Path: res.ConfigPath})
	hotkey := o.Hotkey
	if hotkey == "" && created {
		hotkey = DefaultHotkey
	}
	type edit struct {
		key string
		val any
	}
	edits := []edit{
		{"user.name", o.Name},
		{"personas.default", o.Persona},
		{"ai.provider", o.Provider},
		{"input.dictate.key", hotkey},
	}
	if o.Persona != "" {
		edits = append(edits, edit{"personas.listening", []string{o.Persona}})
	}
	if o.APIKey != "" {
		edits = append(edits, edit{"ai.providers." + o.Provider + ".api_key", "keychain:" + o.Provider})
	}
	for _, e := range edits {
		if s, isStr := e.val.(string); isStr && s == "" {
			continue
		}
		if err := config.SetFile(res.ConfigPath, e.key, e.val); err != nil {
			return res, fmt.Errorf("write %s: %w", res.ConfigPath, err)
		}
	}
	step("config       %s", res.ConfigPath)
	cfg, err := config.Load(res.ConfigPath)
	if err != nil {
		return res, err
	}
	keys := []struct{ key, what string }{
		{cfg.String("input.dictate.key"), "dictation"},
		{cfg.String("input.command.key"), "commands"},
	}
	step("hotkey       push-to-talk on %s (%s to talk)", keys[0].key, cfg.String("input.dictate.mode"))

	// Desktop integration: the files that name Footman to the desktop, the
	// push-to-talk keys bound to exactly the keys in the config, and the
	// permission to type and paste.
	if is, ok := o.Platform.Input().(platform.InputSetup); ok {
		if o.Binary != "" {
			files, err := is.SetupInput(ctx, o.Binary)
			for _, f := range files.Program {
				rec.Add(Entry{Kind: KindFile, Group: GroupProgram, Path: f})
			}
			for _, f := range files.State {
				rec.Add(Entry{Kind: KindFile, Group: GroupData, Path: f})
			}
			if err != nil {
				return res, fmt.Errorf("desktop integration: %w", err)
			}
			for _, f := range files.Program {
				step("desktop      %s", f)
			}
		}
		for _, k := range keys {
			if k.key != "" {
				step("%s", bindLine(ctx, is, k.key, k.what))
			}
		}
		// The permission dialog appears now, while the user is at setup,
		// rather than in the middle of the first dictation.
		if err := is.PrepareInsert(ctx); err != nil {
			step("insert       not allowed yet (%v); the desktop asks again at the first dictation", err)
		} else {
			step("insert       allowed to type and paste at the cursor")
		}
	}

	db := filepath.Join(paths.Data(), DatabaseFile)
	st, err := store.Open(ctx, db)
	if err != nil {
		return res, fmt.Errorf("create database: %w", err)
	}
	if err := st.Close(); err != nil {
		return res, err
	}
	rec.Add(Entry{Kind: KindFile, Group: GroupData, Path: db})
	step("database     %s", db)

	switch {
	case o.SkipModels:
		step("speech       skipped (--skip-models); run `footman models fetch` before the first start")
	case o.Speech != nil:
		err := o.Speech(ctx, SpeechEnv{
			ConfigPath: res.ConfigPath, DataDir: paths.Data(), CacheDir: paths.Cache(), Step: step,
			Record: func(e Entry) error {
				if err := e.validate(); err != nil {
					return err
				}
				rec.Add(e)
				return rec.Save(res.ReceiptPath)
			},
		})
		if err != nil {
			return res, fmt.Errorf("speech models: %w", err)
		}
	}

	if !o.NoService {
		// Recorded first: a half-installed service must still be undone.
		rec.Add(Entry{Kind: KindService, Group: GroupProgram, Name: ServiceName})
		if err := o.Platform.Service().Install(ctx); err != nil {
			return res, fmt.Errorf("install service: %w", err)
		}
		step("service      %s installed, enabled and started", ServiceName)
	}
	return res, nil
}

// bindLine binds a push-to-talk key (for what: "dictation" or "commands")
// with the desktop and describes the result. Each key is bound on its own,
// so one that is taken does not stop the other. Failing to bind does not
// fail setup: the service binds the key again when it starts, and the user
// can set it in the desktop's settings.
func bindLine(ctx context.Context, is platform.InputSetup, hotkey, what string) string {
	got, err := is.BindHotkeys(ctx, []string{hotkey})
	switch {
	case errors.Is(err, platform.ErrUnsupported):
		return fmt.Sprintf("hotkey       %s (%s) not bound: this desktop has no global shortcut service", hotkey, what)
	case err != nil:
		return fmt.Sprintf("hotkey       %s (%s) not bound: %v", hotkey, what, err)
	case len(got) == 0 || got[0].Trigger == "":
		return fmt.Sprintf("hotkey       %s (%s) has no key yet: set it in the desktop's shortcut settings (Footman)", hotkey, what)
	case got[0].Trigger != hotkey:
		return fmt.Sprintf("hotkey       %s (%s) is bound to %s by the desktop", hotkey, what, got[0].Trigger)
	}
	return fmt.Sprintf("hotkey       %s bound with the desktop (%s)", hotkey, what)
}
