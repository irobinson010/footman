package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/models"
)

func init() {
	register(command{name: "transcribe", summary: "transcribe a WAV file with the daemon's speech engines", run: cmdTranscribe})
	register(command{name: "models", summary: "list, choose and download speech models", run: cmdModels})
}

func cmdTranscribe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs, socket := clientFlags("transcribe", "[--json] [--no-vad] FILE.wav", stderr)
	asJSON := fs.Bool("json", false, "print the raw speech.transcribe result")
	noVAD := fs.Bool("no-vad", false, "transcribe the whole file at once instead of splitting it with the VAD")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fail(stderr, err)
	}
	c := dial(ctx, *socket, stderr)
	if c == nil {
		return 1
	}
	defer func() { _ = c.Close() }()
	params := api.TranscribeParams{Path: path}
	if *noVAD {
		f := false
		params.VAD = &f
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "speech.transcribe", params, &raw); err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		_, _ = fmt.Fprintln(stdout, string(raw))
		return 0
	}
	var r api.TranscribeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, r.Text)
	vad := r.VAD
	if vad == "" {
		vad = "none"
	}
	_, _ = fmt.Fprintf(stderr, "%.1f s of audio, %d segment(s); stt %s, vad %s; speech-to-text %.0f ms, VAD %.0f ms, total %.0f ms\n",
		float64(r.DurationMS)/1000, len(r.Segments), r.STT, vad, r.STTMS, r.VADMS, r.TotalMS)
	return 0
}

// modelsEnv is what `footman models` works with.
type modelsEnv struct {
	dir     string
	cfgPath string
	// record adds a model (or, with isDir, the models folder) to the install
	// receipt; nil records nothing.
	record func(path, id string, isDir bool) error
}

func cmdModels(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	paths := defaultPaths()
	fs := flag.NewFlagSet("footman models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	env := modelsEnv{}
	fs.StringVar(&env.dir, "dir", modelsDir(paths.Data), "models folder")
	fs.StringVar(&env.cfgPath, "config", filepath.Join(paths.Config, "config.toml"), "config file (for the default choice)")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, `Usage: footman models [options] <list | default | fetch [ID...]>

  list         the models footman can fetch, and which are downloaded
  default      the detected hardware and the engine, model and accelerator
               "auto" picks on it (ADR-0024)
  fetch        download models, checking their SHA-256, and record them in
               the install receipt; with no IDs, the default model and the VAD

Options:
`)
		fs.PrintDefaults()
	}
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	switch fs.Arg(0) {
	case "list":
		return modelsList(env, stdout)
	case "default":
		return modelsDefault(env, stdout, stderr)
	case "fetch":
		// Only the real models folder goes on the receipt; a folder given
		// with --dir (tests, CI) is the caller's to manage.
		if filepath.Clean(env.dir) == modelsDir(paths.Data) {
			env.record = func(path, id string, isDir bool) error {
				e := install.Entry{Kind: install.KindModel, Group: install.GroupModels, Path: path, Name: id}
				if isDir {
					e = install.Entry{Kind: install.KindDir, Group: install.GroupModels, Path: path}
				}
				return install.RecordIn(paths.Data, e)
			}
		}
		return modelsFetch(ctx, env, fs.Args()[1:], stdout, stderr)
	default:
		fs.Usage()
		return 2
	}
}

func modelsList(env modelsEnv, stdout io.Writer) int {
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tENGINE\tSIZE\tLANGUAGES\tLICENSE\tDOWNLOADED")
	for _, m := range models.Catalog {
		lang := "many"
		if m.English {
			lang = "English"
		}
		have := "no"
		if m.Installed(env.dir) {
			have = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d MB\t%s\t%s\t%s\n", m.ID, m.Engine, m.Size>>20, lang, m.License, have)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(stdout, "\nModels folder: %s\n", env.dir)
	return 0
}

// defaultChoice is what the daemon would pick with the current config.
func defaultChoice(env modelsEnv) (accel.Hardware, accel.Choice, error) {
	cfg, err := config.Load(env.cfgPath)
	if err != nil {
		return accel.Hardware{}, accel.Choice{}, err
	}
	hw := accel.Detect()
	c, err := accel.Choose(hw, accel.Request{
		Engine:      cfg.String("speech.stt.engine"),
		Model:       cfg.String("speech.stt.model"),
		Accelerator: cfg.String("speech.stt.accelerator"),
		Language:    cfg.String("user.language"),
	}, accel.Engines())
	return hw, c, err
}

func modelsDefault(env modelsEnv, stdout, stderr io.Writer) int {
	hw, c, err := defaultChoice(env)
	_, _ = fmt.Fprintf(stdout, "hardware: %s\n", hw)
	engines := accel.Engines()
	var names []string
	for n, accs := range engines {
		names = append(names, n+" ["+strings.Join(accs, " ")+"]")
	}
	sort.Strings(names)
	if len(names) == 0 {
		names = []string{"none (this build has no native engines; build with -tags native)"}
	}
	_, _ = fmt.Fprintf(stdout, "engines:  %s\n", strings.Join(names, ", "))
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "default:  %s\n", c)
	return 0
}

func modelsFetch(ctx context.Context, env modelsEnv, ids []string, stdout, stderr io.Writer) int {
	if len(ids) == 0 {
		_, c, err := defaultChoice(env)
		if err != nil {
			return fail(stderr, fmt.Errorf("%w; name the models to fetch (see `footman models list`)", err))
		}
		ids = []string{c.Model, defaultVADModel}
	}
	for _, id := range ids {
		m, ok := models.Lookup(id)
		if !ok {
			return fail(stderr, fmt.Errorf("unknown model %q (see `footman models list`)", id))
		}
		var record func(string, bool) error
		if env.record != nil {
			record = func(path string, isDir bool) error { return env.record(path, id, isDir) }
		}
		had := m.Installed(env.dir)
		start, last, lastDone := time.Now(), time.Time{}, int64(-1)
		p, err := models.Fetch(ctx, m, env.dir, models.FetchOptions{
			Record: record,
			Progress: func(done, total int64) {
				if done == lastDone || (time.Since(last) < time.Second && done < total) {
					return
				}
				last, lastDone = time.Now(), done
				_, _ = fmt.Fprintf(stderr, "\r%s: %3d%% of %.1f MB", id, 100*done/max(total, 1), float64(total)/(1<<20))
			},
		})
		if !had {
			_, _ = fmt.Fprintln(stderr)
		}
		if err != nil {
			return fail(stderr, err)
		}
		if had {
			_, _ = fmt.Fprintf(stdout, "%s: already downloaded (%s)\n", id, p)
			continue
		}
		_, _ = fmt.Fprintf(stdout, "%s: %s (%s)\n", id, p, time.Since(start).Round(time.Second))
	}
	return 0
}

// defaultVADModel is the model the default VAD (Silero) loads.
const defaultVADModel = "silero-vad"
