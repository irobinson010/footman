package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/store"
)

func init() {
	register(command{name: "daemon", summary: "run the footman service (normally started by the service manager)", run: cmdDaemon})
}

// fakeName is the registry name every area's fake registers under (ADR-0021).
const fakeName = "fake"

// Names of the speech engines that have no config key yet. The sherpa-onnx
// engines (design §24, speech/sherpa) will register under these.
const (
	defaultVAD  = "sherpa"
	defaultWake = "sherpa"
)

// daemonOptions configure runDaemon.
type daemonOptions struct {
	socket     string
	configPath string
	dataDir    string
	// devFakes lets areas without a registered real implementation fall
	// back to their fakes, so the daemon runs on a machine (or in a build)
	// without platform or speech support. For development and tests only.
	devFakes bool

	// Test hooks.
	// deps, if set, edits the pipeline's dependencies after wiring, so a
	// test can swap in scripted fakes.
	deps func(*core.Deps)
	// ready, if set, is called once the socket is accepting connections.
	ready func()
}

func cmdDaemon(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	paths := defaultPaths()
	fs := flag.NewFlagSet("footman daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o daemonOptions
	fs.StringVar(&o.socket, "socket", defaultSocket(), "Unix socket to serve the local API on (or set $"+socketEnv+")")
	fs.StringVar(&o.configPath, "config", filepath.Join(paths.Config, "config.toml"), "config file")
	fs.StringVar(&o.dataDir, "data", paths.Data, "data directory (footman.db)")
	fs.BoolVar(&o.devFakes, "dev-fakes", false, "DEVELOPMENT ONLY: use fake platform, speech, AI and worker implementations wherever no real one is registered")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "Usage: footman daemon [options]\n\nRuns the footman service in the foreground until SIGTERM or Ctrl-C.\n\nOptions:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	if err := runDaemon(ctx, o, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, "footman daemon:", err)
		return 1
	}
	return 0
}

// runDaemon wires config → store → bus → core pipeline → local API and serves
// until ctx is cancelled, then shuts down in reverse order.
func runDaemon(ctx context.Context, o daemonOptions, logw io.Writer) (err error) {
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(logw, "footman daemon: "+format+"\n", args...) }
	started := time.Now()

	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.configPath), 0o700); err != nil {
		return fmt.Errorf("config directory: %w", err)
	}
	cfgFile := api.NewConfigFile(o.configPath, cfg)

	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	st, err := store.Open(ctx, filepath.Join(o.dataDir, "footman.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, st.Close()) }()

	b := bus.New()
	defer b.Close()

	deps, impl, err := wire(cfg, o.devFakes)
	if err != nil {
		return err
	}
	deps.Bus = b
	if o.deps != nil {
		o.deps(&deps)
	}
	if o.devFakes {
		logf("WARNING: running with --dev-fakes; fakes stand in for: %v", fakedAreas(impl))
	}

	p, err := core.New(deps, pipelineConfig(cfg))
	if err != nil {
		return err
	}
	defer p.Close()

	var bg sync.WaitGroup
	defer bg.Wait()
	bctx, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBG()

	// History: keep transcripts when the config says so.
	transcripts := bus.Subscribe(b, core.Transcribed, 64)
	defer transcripts.Close()
	bg.Go(func() {
		for ev := range transcripts.C() {
			if cfgFile.Current().Bool("history.transcripts") {
				if _, err := st.AddHistory(bctx, "transcript", ev.Data.Text); err != nil {
					logf("history: %v", err)
				}
			}
		}
	})

	// Pick up hand edits to the config file. Pipeline settings are read at
	// start for now; the API sees the new values at once.
	bg.Go(func() {
		config.Watch(bctx, o.configPath, config.WatchOptions{}, func(c *config.Config, err error) {
			if err != nil {
				logf("config not reloaded: %v", err)
				return
			}
			cfgFile.Replace(c)
		})
	})

	// The microphone and hotkeys, when there is a VAD to split speech.
	if deps.VAD != nil {
		bg.Go(func() {
			if err := p.Run(bctx); err != nil && !errors.Is(err, context.Canceled) {
				logf("pipeline stopped: %v", err)
			} else if err == nil {
				logf("microphone stream ended; the API keeps serving")
			}
		})
	} else {
		logf("no VAD available: the microphone is not used; text commands still work")
	}

	srv := api.NewServer()
	api.Register(srv, api.Deps{Pipeline: p, Bus: b, Config: cfgFile, Info: api.Info{
		Version: version, Started: started, DevFakes: o.devFakes, Implementations: impl,
		Socket: o.socket, ConfigPath: o.configPath,
	}})
	l, err := api.Listen(o.socket)
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()
	logf("listening on %s", o.socket)
	if o.ready != nil {
		o.ready()
	}

	select {
	case <-ctx.Done():
		logf("shutting down")
	case err := <-served:
		srv.Close()
		return fmt.Errorf("local API stopped: %w", err)
	}
	// Reverse order: stop taking requests (cancelling those in flight),
	// then the microphone loop and background work; the deferred calls
	// close the pipeline (cancelling jobs), the bus and the store.
	srv.Close()
	if err := <-served; !errors.Is(err, api.ErrServerClosed) {
		logf("local API: %v", err)
	}
	stopBG()
	return nil
}

// wire picks an implementation for every area from the registries: the one
// the config names when it is registered, otherwise (with devFakes) the fake.
// It returns the pipeline dependencies (without the bus) and, per area, the
// name of what it chose ("" for none).
func wire(cfg *config.Config, devFakes bool) (core.Deps, map[string]string, error) {
	impl := map[string]string{}
	var d core.Deps
	var err error

	// Real implementations get their options from config once they exist;
	// until then every factory gets none.
	if d.Platform, impl["platform"], err = pick(platform.Platforms, runtime.GOOS, devFakes); err != nil {
		return d, nil, err
	}
	if d.STT, impl["stt"], err = pick(speech.STTs, cfg.String("speech.stt.engine"), devFakes); err != nil {
		return d, nil, err
	}
	if d.VAD, impl["vad"], err = pick(speech.VADs, defaultVAD, devFakes); err != nil {
		return d, nil, err
	}
	if cfg.Bool("input.wake_word") {
		if d.Wake, impl["wake"], err = pick(speech.WakeWords, defaultWake, devFakes); err != nil {
			return d, nil, err
		}
	} else {
		impl["wake"] = ""
	}
	if d.TTS, impl["tts"], err = pick(speech.TTSs, cfg.String("speech.tts.engine"), devFakes); err != nil {
		return d, nil, err
	}
	if d.LLM, impl["llm"], err = pick(llm.Providers, cfg.String("ai.provider"), devFakes); err != nil {
		return d, nil, err
	}
	if d.Worker, impl["worker"], err = pick(jobs.Workers, cfg.String("jobs.worker"), devFakes); err != nil {
		return d, nil, err
	}

	// The pipeline cannot run without a platform and speech-to-text.
	for _, area := range []struct {
		key, what string
		names     []string
	}{
		{"platform", "platform " + runtime.GOOS, platform.Platforms.Names()},
		{"stt", "speech-to-text engine " + cfg.String("speech.stt.engine"), speech.STTs.Names()},
	} {
		if impl[area.key] == "" {
			return d, nil, fmt.Errorf("this build has no %s (registered: %v); "+
				"for development, run with --dev-fakes to use fakes", area.what, area.names)
		}
	}
	return d, impl, nil
}

// pick builds the implementation registered as want, or the fake when want
// is not registered and devFakes is set. It returns the zero T and "" when
// neither is available.
func pick[T any](reg *registry.Registry[T], want string, devFakes bool) (T, string, error) {
	name := ""
	switch {
	case want != "" && reg.Has(want):
		name = want
	case devFakes && reg.Has(fakeName):
		name = fakeName
	default:
		var zero T
		return zero, "", nil
	}
	v, err := reg.New(name, nil)
	return v, name, err
}

func fakedAreas(impl map[string]string) []string {
	var out []string
	for _, a := range []string{"platform", "stt", "vad", "wake", "tts", "llm", "worker"} {
		if impl[a] == fakeName {
			out = append(out, a)
		}
	}
	return out
}

// pipelineConfig maps config keys to the pipeline's settings.
func pipelineConfig(cfg *config.Config) core.Config {
	mic := cfg.String("input.microphone")
	if mic == "default" {
		mic = ""
	}
	return core.Config{
		DictateHotkey: cfg.String("input.dictate.key"),
		CommandHotkey: cfg.String("input.command.key"),
		Mic:           mic,
		Insert:        dictation.InsertMode(cfg.String("dictation.insert")),
	}
}
