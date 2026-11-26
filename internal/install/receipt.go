package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/platform"
)

// ReceiptFile is the receipt's file name inside the data directory.
const ReceiptFile = "install-receipt.json"

// ReceiptVersion is the current receipt format.
const ReceiptVersion = 1

// ErrNoReceipt is returned (wrapped) when there is no install receipt.
var ErrNoReceipt = errors.New("install: no install receipt")

// Kind says what an Entry is and how it is removed.
type Kind string

// Entry kinds.
const (
	// KindFile is one file. For a ".db" file its -wal, -shm and -journal
	// companions go with it.
	KindFile Kind = "file"
	// KindDir is a folder Footman made. It is removed only once it is empty;
	// anything else in it is not ours and is left alone.
	KindDir Kind = "dir"
	// KindTree is a folder Footman filled, such as a persona or plugin. It is
	// removed with everything in it.
	KindTree Kind = "tree"
	// KindModel is a downloaded model, a file or a folder, removed whole.
	KindModel Kind = "model"
	// KindService is the background service (the systemd user unit on
	// Linux), removed through platform.Service. Name is the unit name.
	KindService Kind = "service"
	// KindDesktop is a grant or registration made with the desktop, removed
	// through platform.InputReset. Name is platform.ResetPermissions or
	// platform.ResetShortcuts.
	KindDesktop Kind = "desktop"
	// KindSecret is a keychain entry, removed through platform.Secrets. Name
	// is the key. The value is never recorded.
	KindSecret Kind = "secret"
)

// Group is a heading in the uninstall listing (design §11).
type Group string

// Groups, in listing order.
const (
	GroupProgram  Group = "program"
	GroupModels   Group = "models"
	GroupConfig   Group = "config"
	GroupData     Group = "data"
	GroupPersonas Group = "personas"
	GroupSecrets  Group = "secrets"
	GroupCache    Group = "cache"
)

// Groups lists every group in listing order.
var Groups = []Group{GroupProgram, GroupModels, GroupConfig, GroupData, GroupPersonas, GroupSecrets, GroupCache}

// Label is the group's heading in the listing.
func (g Group) Label() string {
	if g == "" {
		return ""
	}
	return strings.ToUpper(string(g[:1])) + string(g[1:])
}

// Entry is one thing Footman installed.
type Entry struct {
	Kind  Kind   `json:"kind"`
	Group Group  `json:"group"`
	Path  string `json:"path,omitempty"`
	Name  string `json:"name,omitempty"`
}

func (e Entry) usesPath() bool {
	return e.Kind != KindService && e.Kind != KindSecret && e.Kind != KindDesktop
}

// validate checks an entry read from a receipt, which is a file the user could
// have edited, before anything is removed on its word.
func (e Entry) validate() error {
	switch e.Kind {
	case KindFile, KindDir, KindTree, KindModel, KindService, KindSecret, KindDesktop:
	default:
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	known := false
	for _, g := range Groups {
		known = known || g == e.Group
	}
	if !known {
		return fmt.Errorf("unknown group %q", e.Group)
	}
	if e.usesPath() {
		return safePath(e.Path)
	}
	if e.Name == "" {
		return fmt.Errorf("%s entry has no name", e.Kind)
	}
	return nil
}

// safePath refuses paths that no uninstall should ever delete: relative ones,
// the filesystem root and the home folder or anything above it.
func safePath(p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("path %q is not a clean absolute path", p)
	}
	if filepath.Dir(p) == p {
		return fmt.Errorf("path %q is a filesystem root", p)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		home = filepath.Clean(home)
		if p == home || strings.HasPrefix(home, p+string(filepath.Separator)) {
			return fmt.Errorf("path %q is the home folder or above it", p)
		}
	}
	return nil
}

// Receipt records everything Footman installed.
type Receipt struct {
	Version     int       `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
	Entries     []Entry   `json:"entries"`
}

// NewReceipt returns an empty receipt stamped now.
func NewReceipt() *Receipt {
	return &Receipt{Version: ReceiptVersion, InstalledAt: time.Now().UTC().Truncate(time.Second)}
}

// ReceiptPath is where the receipt lives for the given directories.
func ReceiptPath(p platform.Paths) string { return filepath.Join(p.Data(), ReceiptFile) }

// Add records e unless an identical entry is already there.
func (r *Receipt) Add(e Entry) {
	if e.usesPath() {
		e.Path = filepath.Clean(e.Path)
	}
	for _, x := range r.Entries {
		if x == e {
			return
		}
	}
	r.Entries = append(r.Entries, e)
}

// Has reports whether an entry of the given kind and group is recorded.
func (r *Receipt) Has(kind Kind, group Group) bool {
	for _, e := range r.Entries {
		if e.Kind == kind && e.Group == group {
			return true
		}
	}
	return false
}

// LoadReceipt reads the receipt at path. A missing file returns an error
// wrapping ErrNoReceipt; an entry that is not safe to act on is an error.
func LoadReceipt(path string) (*Receipt, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", ErrNoReceipt, path)
	}
	if err != nil {
		return nil, err
	}
	var r Receipt
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("install receipt %s: %w", path, err)
	}
	if r.Version != ReceiptVersion {
		return nil, fmt.Errorf("install receipt %s: unsupported version %d", path, r.Version)
	}
	for i, e := range r.Entries {
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("install receipt %s: entry %d: %w", path, i+1, err)
		}
	}
	return &r, nil
}

// Save writes the receipt atomically, creating the folder if needed.
func (r *Receipt) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Chmod(0o600)
	clerr := tmp.Close()
	if err := errors.Join(werr, cerr, clerr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Record adds entries to the install receipt, creating it if there is none.
// Components that install things (the daemon, plugin and persona installs,
// the model downloader) call it so that uninstall knows about them.
func Record(p platform.Platform, entries ...Entry) error {
	return RecordIn(p.Paths().Data(), entries...)
}

// RecordIn is Record for the receipt in the data directory dataDir, for
// callers that know the directory but have no platform (the daemon and the
// CLI work out their directories without one, ADR-0025).
func RecordIn(dataDir string, entries ...Entry) error {
	path := filepath.Join(dataDir, ReceiptFile)
	r, err := LoadReceipt(path)
	if errors.Is(err, ErrNoReceipt) {
		r = NewReceipt()
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if err := e.validate(); err != nil {
			return err
		}
		r.Add(e)
	}
	return r.Save(path)
}
