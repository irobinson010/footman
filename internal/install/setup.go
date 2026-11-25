package install

import (
	"context"
	"errors"
	"fmt"
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
	step := func(format string, a ...any) { res.Lines = append(res.Lines, fmt.Sprintf(format, a...)) }

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
	if hotkey != "" {
		step("hotkey       push-to-talk on %s", hotkey)
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

	if !o.NoService {
		// Recorded first: a half-installed service must still be undone.
		rec.Add(Entry{Kind: KindService, Group: GroupProgram, Name: ServiceName})
		if err := o.Platform.Service().Install(ctx); err != nil {
			return res, fmt.Errorf("install service: %w", err)
		}
		step("service      %s installed and enabled", ServiceName)
	}
	return res, nil
}
