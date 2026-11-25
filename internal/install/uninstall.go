package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irobinson010/footman/internal/platform"
)

// UninstallOptions chooses what to remove. The zero value removes the program,
// service, models, cache, config, data and personas, and keeps the keychain.
type UninstallOptions struct {
	// Platform supplies directories, the keychain and the service.
	Platform platform.Platform
	// Purge removes everything, keychain secrets included.
	Purge bool
	// KeepConfig keeps the Config group.
	KeepConfig bool
	// KeepData keeps the Data and Personas groups, and the receipt, so a
	// reinstall picks up where it left off.
	KeepData bool
	// RemoveSecrets is the explicit confirmation to delete keychain entries.
	// Without it (or Purge) they are kept.
	RemoveSecrets bool
	// DryRun reports what would happen and changes nothing.
	DryRun bool
}

func (o UninstallOptions) check() error {
	if o.Purge && (o.KeepConfig || o.KeepData) {
		return errors.New("install: purge removes everything, so it cannot be combined with keep-config or keep-data")
	}
	return nil
}

// keeps reports whether group g survives these options. The program, service,
// models and cache are always removed.
func (o UninstallOptions) keeps(g Group) bool {
	switch g {
	case GroupConfig:
		return o.KeepConfig
	case GroupData, GroupPersonas:
		return o.KeepData
	case GroupSecrets:
		return !o.Purge && !o.RemoveSecrets
	}
	return false
}

// PlanItem is one receipt entry and what it takes up.
type PlanItem struct {
	Entry
	// Size is the bytes on disk (0 for secrets, services and folders that
	// only contain other entries).
	Size int64
}

// PlanGroup is the entries under one heading of the listing.
type PlanGroup struct {
	Group Group
	Items []PlanItem
	Size  int64
}

// Plan is what the receipt says is installed, grouped as design §11 lists it.
type Plan struct {
	// Receipt is the receipt it was built from.
	Receipt *Receipt
	// ReceiptPath is where the receipt is.
	ReceiptPath string
	// Groups holds the non-empty groups in listing order.
	Groups []PlanGroup
	p      platform.Platform
}

// LoadPlan reads the receipt and measures what is installed. It changes
// nothing. A missing receipt returns an error wrapping ErrNoReceipt.
func LoadPlan(p platform.Platform) (*Plan, error) {
	path := ReceiptPath(p.Paths())
	rec, err := LoadReceipt(path)
	if err != nil {
		return nil, err
	}
	pl := &Plan{Receipt: rec, ReceiptPath: path, p: p}
	for _, g := range Groups {
		pg := PlanGroup{Group: g}
		for _, e := range rec.Entries {
			if e.Group != g {
				continue
			}
			it := PlanItem{Entry: e}
			if e.Kind == KindFile || e.Kind == KindTree || e.Kind == KindModel {
				it.Size = sizeOf(e)
			}
			pg.Size += it.Size
			pg.Items = append(pg.Items, it)
		}
		if len(pg.Items) > 0 {
			pl.Groups = append(pl.Groups, pg)
		}
	}
	return pl, nil
}

// Count returns how many entries of the given kind the group has.
func (pl *Plan) Count(g Group, k Kind) int {
	n := 0
	for _, pg := range pl.Groups {
		if pg.Group != g {
			continue
		}
		for _, it := range pg.Items {
			if it.Kind == k {
				n++
			}
		}
	}
	return n
}

// Listing writes the grouped listing with sizes (design §11 step 2).
func (pl *Plan) Listing(w io.Writer) {
	for _, pg := range pl.Groups {
		what := pg.summary()
		size := ""
		if pg.Size > 0 {
			size = humanSize(pg.Size)
		}
		_, _ = fmt.Fprintf(w, "  %-9s %-50s %9s\n", pg.Group.Label(), what, size)
	}
}

func (pg PlanGroup) summary() string {
	if pg.Group == GroupSecrets {
		n := 0
		for _, it := range pg.Items {
			if it.Kind == KindSecret {
				n++
			}
		}
		noun := "API keys"
		if n == 1 {
			noun = "API key"
		}
		return fmt.Sprintf("%d %s in the OS keychain", n, noun)
	}
	var names []string
	seen := map[string]bool{}
	for _, it := range pg.Items {
		var n string
		switch {
		case it.Kind == KindDir:
			continue
		case it.Kind == KindService:
			n = "service unit"
		case it.Kind == KindDesktop:
			n = "desktop shortcuts and permissions"
		case it.Name != "":
			n = it.Name
		default:
			n = filepath.Base(it.Path)
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "folders"
	}
	return strings.Join(names, ", ")
}

// Report says what an uninstall did (or, for a dry run, would do).
type Report struct {
	// DryRun is true when nothing was changed.
	DryRun bool
	// Removed lists what was (or would be) removed.
	Removed []string
	// Kept lists what was kept on purpose, with its path.
	Kept []string
	// Left lists folders that were not removed because they hold files that
	// Footman did not install.
	Left []string
}

// Write prints the summary (design §11 step 5).
func (r *Report) Write(w io.Writer) {
	verb := "Removed"
	if r.DryRun {
		verb = "Would remove"
		_, _ = fmt.Fprintln(w, "Dry run: nothing was changed.")
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		_, _ = fmt.Fprintf(w, "%s:\n", title)
		for _, l := range lines {
			_, _ = fmt.Fprintf(w, "  %s\n", l)
		}
	}
	section(verb, r.Removed)
	section("Kept", r.Kept)
	section("Left alone (not installed by Footman)", r.Left)
}

func describe(e Entry) string {
	switch e.Kind {
	case KindService:
		return "service " + e.Name
	case KindSecret:
		return "keychain entry " + e.Name
	case KindDesktop:
		return "desktop " + e.Name
	}
	return e.Path
}

// Uninstall loads the plan and runs it.
func Uninstall(ctx context.Context, o UninstallOptions) (*Report, error) {
	if o.Platform == nil {
		return nil, errors.New("install: uninstall needs a platform")
	}
	pl, err := LoadPlan(o.Platform)
	if err != nil {
		return nil, err
	}
	return pl.Run(ctx, o)
}

// Run removes everything the options do not keep, working only from the
// receipt. Failures are collected and the rest still runs; whatever could not
// be removed stays on the receipt (when it is kept) so a second run finishes
// the job.
func (pl *Plan) Run(ctx context.Context, o UninstallOptions) (*Report, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	rep := &Report{DryRun: o.DryRun}
	var remaining []Entry // what stays on the receipt
	var errs []error
	var dirs []Entry

	for _, pg := range pl.Groups {
		keep := o.keeps(pg.Group)
		for _, it := range pg.Items {
			e := it.Entry
			if keep {
				if e.Kind != KindDir {
					rep.Kept = append(rep.Kept, describe(e))
				}
				remaining = append(remaining, e)
				continue
			}
			if o.DryRun {
				if e.Kind != KindDir {
					rep.Removed = append(rep.Removed, describe(e))
				}
				continue
			}
			if e.Kind == KindDir {
				dirs = append(dirs, e)
				continue
			}
			if err := pl.remove(ctx, e); err != nil {
				errs = append(errs, err)
				remaining = append(remaining, e)
				continue
			}
			rep.Removed = append(rep.Removed, describe(e))
		}
	}
	if o.DryRun {
		return rep, nil
	}

	// The receipt lives in the data folder; it must go before that folder can.
	keepReceipt := o.keeps(GroupData)
	if !keepReceipt {
		if err := os.Remove(pl.ReceiptPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	// Deepest folders first, so a parent is empty by the time it is tried.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].Path) > len(dirs[j].Path) })
	for _, e := range dirs {
		err := os.Remove(e.Path)
		switch {
		case err == nil:
			rep.Removed = append(rep.Removed, e.Path)
		case errors.Is(err, fs.ErrNotExist):
		case isNotEmpty(e.Path):
			rep.Left = append(rep.Left, e.Path)
			remaining = append(remaining, e)
		default:
			errs = append(errs, err)
			remaining = append(remaining, e)
		}
	}
	if keepReceipt {
		pl.Receipt.Entries = remaining
		if err := pl.Receipt.Save(pl.ReceiptPath); err != nil {
			errs = append(errs, err)
		}
	}
	return rep, errors.Join(errs...)
}

func isNotEmpty(dir string) bool {
	ents, err := os.ReadDir(dir)
	return err == nil && len(ents) > 0
}

func (pl *Plan) remove(ctx context.Context, e Entry) error {
	switch e.Kind {
	case KindService:
		return pl.p.Service().Uninstall(ctx)
	case KindSecret:
		return pl.p.Secrets().Delete(ctx, e.Name)
	case KindDesktop:
		if r, ok := pl.p.Input().(platform.InputReset); ok {
			return r.ResetInput(ctx, e.Name)
		}
		return nil
	case KindTree, KindModel:
		return os.RemoveAll(e.Path)
	case KindFile:
		for _, p := range fileSet(e.Path) {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// fileSet is a file plus the SQLite companions that travel with a ".db" file.
func fileSet(path string) []string {
	if strings.HasSuffix(path, ".db") {
		return []string{path, path + "-wal", path + "-shm", path + "-journal"}
	}
	return []string{path}
}

func sizeOf(e Entry) int64 {
	var n int64
	if e.Kind == KindFile {
		for _, p := range fileSet(e.Path) {
			if st, err := os.Lstat(p); err == nil {
				n += st.Size()
			}
		}
		return n
	}
	_ = filepath.WalkDir(e.Path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func humanSize(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%d KB", (n+k/2)/k)
	case n < k*k*k:
		return fmt.Sprintf("%d MB", (n+k*k/2)/(k*k))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
}
