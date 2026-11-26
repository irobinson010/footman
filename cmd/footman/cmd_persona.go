package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/persona"
	"github.com/irobinson010/footman/personas"
)

func init() {
	register(command{name: "persona", summary: "list, install or remove persona packs (`footman persona list`)", run: func(_ context.Context, args []string, stdout, stderr io.Writer) int {
		return cmdPersona(args, stdout, stderr)
	}})
}

const personaUsage = "usage: footman persona list | install [--replace] <dir> | remove <id>\n"

// personaDataDir is a test seam for the data folder.
var personaDataDir = func() string { return defaultPaths().Data }

func personaCatalog() *persona.Catalog {
	return persona.NewCatalog(personas.FS, filepath.Join(personaDataDir(), "personas"))
}

// cmdPersona is `footman persona` (design §27).
func cmdPersona(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, personaUsage)
		return 2
	}
	cat := personaCatalog()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			_, _ = fmt.Fprint(stderr, personaUsage)
			return 2
		}
		return personaList(cat, stdout)
	case "install":
		fs := newFlagSet("persona install", stderr)
		replace := fs.Bool("replace", false, "replace an installed pack with the same ID")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() != 1 {
			_, _ = fmt.Fprint(stderr, personaUsage)
			return 2
		}
		p, dest, err := cat.Install(fs.Arg(0), *replace)
		if err != nil {
			return fail(stderr, err)
		}
		dir := filepath.Dir(dest)
		if err := install.RecordIn(personaDataDir(),
			install.Entry{Kind: install.KindDir, Group: install.GroupPersonas, Path: dir},
			install.Entry{Kind: install.KindTree, Group: install.GroupPersonas, Path: dest, Name: p.ID},
		); err != nil {
			return fail(stderr, fmt.Errorf("installed %s but could not record it in the install receipt: %w", dest, err))
		}
		_, _ = fmt.Fprintf(stdout, "Installed %s (%s) in %s\n", p.Name, p.ID, dest)
		return 0
	case "remove":
		if len(args) != 2 {
			_, _ = fmt.Fprint(stderr, personaUsage)
			return 2
		}
		if err := cat.Remove(args[1]); err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintf(stdout, "Removed %s\n", args[1])
		return 0
	}
	_, _ = fmt.Fprint(stderr, personaUsage)
	return 2
}

func personaList(cat *persona.Catalog, stdout io.Writer) int {
	def := "ashford"
	if cfg, err := config.Load(filepath.Join(defaultPaths().Config, "config.toml")); err == nil {
		def = cfg.String("personas.default")
	}
	bad := 0
	for _, i := range cat.List() {
		mark := " "
		if i.ID == def {
			mark = "*"
		}
		if i.Err != nil {
			bad++
			_, _ = fmt.Fprintf(stdout, "%s %-12s %-8s INVALID: %v\n", mark, i.ID, i.Source, i.Err)
			continue
		}
		src := string(i.Source)
		if i.Overrides {
			src += "*"
		}
		_, _ = fmt.Fprintf(stdout, "%s %-12s %-8s %-10s %s\n", mark, i.ID, src, i.Pack.Voice.Engine, i.Pack.Description)
	}
	_, _ = fmt.Fprintln(stdout, "(* default persona; user* overrides a shipped pack)")
	if bad > 0 {
		return 1
	}
	return 0
}
