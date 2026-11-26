package persona

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Source says where a pack came from.
type Source string

// Pack sources.
const (
	Shipped Source = "shipped"
	User    Source = "user"
)

// Catalog finds packs: those embedded in the binary and those in a user
// folder (<data>/personas). A user pack with the same ID overrides the
// shipped one.
type Catalog struct {
	shipped fs.FS
	userDir string
}

// NewCatalog returns a Catalog over shipped (one folder per pack; may be nil)
// and userDir (may be empty or missing).
func NewCatalog(shipped fs.FS, userDir string) *Catalog {
	return &Catalog{shipped: shipped, userDir: userDir}
}

// Info describes one pack found by List. A pack that fails to load has Err
// set and only ID and Source filled in, so one broken pack never hides the
// others.
type Info struct {
	ID     string
	Source Source
	Pack   *Pack
	Err    error
	// Overrides is set on a user pack that replaces a shipped one.
	Overrides bool
}

// List returns every pack, sorted by ID, with user packs replacing shipped
// ones of the same ID.
func (c *Catalog) List() []Info {
	byID := map[string]Info{}
	if c.shipped != nil {
		ents, _ := fs.ReadDir(c.shipped, ".")
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			sub, err := fs.Sub(c.shipped, e.Name())
			if err != nil {
				byID[e.Name()] = Info{ID: e.Name(), Source: Shipped, Err: err}
				continue
			}
			p, err := Load(sub, e.Name())
			if err == nil && p.ID != e.Name() {
				err = fmt.Errorf("persona.id %q does not match its folder %q", p.ID, e.Name())
			}
			byID[e.Name()] = Info{ID: e.Name(), Source: Shipped, Pack: p, Err: err}
		}
	}
	if c.userDir != "" {
		ents, _ := os.ReadDir(c.userDir)
		for _, e := range ents {
			if !e.IsDir() || !ValidID(e.Name()) {
				continue // skips stray files and in-progress installs (".tmp-…")
			}
			p, err := LoadDir(filepath.Join(c.userDir, e.Name()))
			if err == nil && p.ID != e.Name() {
				p, err = nil, fmt.Errorf("persona.id %q does not match its folder %q", p.ID, e.Name())
			}
			_, over := byID[e.Name()]
			byID[e.Name()] = Info{ID: e.Name(), Source: User, Pack: p, Err: err, Overrides: over}
		}
	}
	out := make([]Info, 0, len(byID))
	for _, i := range byID {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// ErrNotFound is returned (wrapped) for an unknown persona ID.
var ErrNotFound = errors.New("persona: not found")

// Get returns the pack with the given ID.
func (c *Catalog) Get(id string) (*Pack, error) {
	for _, i := range c.List() {
		if i.ID == id {
			if i.Err != nil {
				return nil, i.Err
			}
			return i.Pack, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
}

// Install validates the pack in srcDir and copies it to <userDir>/<id>. It
// refuses to replace an installed pack unless replace is set. The copy is made
// in a temporary folder and renamed into place, so a failed install leaves
// nothing behind. It returns the pack and where it now lives.
func (c *Catalog) Install(srcDir string, replace bool) (*Pack, string, error) {
	if c.userDir == "" {
		return nil, "", errors.New("persona: no user folder to install into")
	}
	p, err := LoadDir(srcDir)
	if err != nil {
		return nil, "", err
	}
	dest := filepath.Join(c.userDir, p.ID)
	if _, err := os.Lstat(dest); err == nil && !replace {
		return nil, "", fmt.Errorf("persona %q is already installed in %s (use --replace)", p.ID, dest)
	}
	if err := os.MkdirAll(c.userDir, 0o755); err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp(c.userDir, ".tmp-"+p.ID+"-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := copyPack(p, tmp); err != nil {
		return nil, "", err
	}
	if replace {
		if err := os.RemoveAll(dest); err != nil {
			return nil, "", err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		return nil, "", err
	}
	// Reload from the installed copy so the Pack reads from its final home.
	p, err = LoadDir(dest)
	return p, dest, err
}

func copyPack(p *Pack, dst string) error {
	for _, name := range p.Files() {
		out := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		in, err := p.fsys.Open(name)
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(w, in)
		_ = in.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes an installed user pack. Shipped packs cannot be removed
// (a user pack that overrides one can; the shipped one shows again).
func (c *Catalog) Remove(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("persona: %q is not a persona ID", id)
	}
	dir := filepath.Join(c.userDir, id)
	if c.userDir == "" {
		dir = ""
	}
	if st, err := os.Lstat(dir); dir == "" || err != nil || !st.IsDir() {
		for _, i := range c.List() {
			if i.ID == id && i.Source == Shipped {
				return fmt.Errorf("persona %q is built in and cannot be removed", id)
			}
		}
		return fmt.Errorf("%w: %q is not installed", ErrNotFound, id)
	}
	return os.RemoveAll(dir)
}
