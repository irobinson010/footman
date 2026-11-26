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
	"github.com/irobinson010/footman/internal/llm/claude"
	"github.com/irobinson010/footman/internal/platform"
	"github.com/irobinson010/footman/internal/registry"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/sherpa"
	"github.com/irobinson010/footman/internal/store"
)

func init() {
	register(command{name: "daemon", summary: "run the footman service (normally started by the service manager)", run: cmdDaemon})
}

// fakeName is the registry name every area's fake registers under (ADR-0021).
const fakeName = "fake"

// defaultWake is the wake-word engine (no config key yet): the sherpa-onnx
// keyword spotter.
const defaultWake = sherpa.KWSName

// daemonOptions configure runDaemon.
type daemonOptions struct {
	socket     string
	configPath string
	dataDir    string
	// cacheDir is the cache folder (the Vulkan shader cache goes in it);
	// empty means the default.
	cacheDir string
	// modelsDir is the speech models folder; empty means <data>/models.
	modelsDir string
	// devFakes lets areas without a registered real implementation fall
	// back to their fakes, so the daemon runs on a machine (or in a build)
	// without platform or speech support. For development and tests only.
	devFakes bool

	// Test hooks.
	// fakesOnly uses the fakes even where a real implementation is
	// registered, so tests never touch the desktop.
	fakesOnly bool
	// realSpeech, with fakesOnly, still uses the real speech-to-text and
	// VAD engines (for the native tests).
	realSpeech bool
	// hardware, if set, replaces accelerator detection.
	hardware func() accel.Hardware
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
	fs.StringVar(&o.dataDir, "data", paths.Data, "data directory (footman.db, models/)")
	o.cacheDir = paths.Cache
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

	// Refuse a socket path the OS cannot bind before starting anything, so
	// the microphone is not opened for a daemon that cannot serve.
	if err := api.CheckSocketPath(o.socket); err != nil {
		return err
	}
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

	deps, impl, reload, err := wire(cfg, o, logf)
	if err != nil {
		return err
	}
	// Closed after the pipeline: this ends the desktop sessions (on Linux,
	// the RemoteDesktop portal session and the hotkeys' portal connection).
	defer closeIfCloser(deps.Platform)
	defer closeIfCloser(deps.Wake)
	logSpeech(logf, o.dataDir, deps.STT, deps.VAD, deps.Wake)
	// Speech models unload when idle and load again on use
	// (speech.unload_after, ADR-0028).
	models := manageSpeech(&deps, impl, reload, func() time.Duration { return unloadAfter(cfgFile.Current()) })
	defer models.Close()
	deps.Bus = b
	if o.deps != nil {
		o.deps(&deps)
	}
	if o.devFakes {
		logf("WARNING: running with --dev-fakes; fakes stand in for: %v", fakedAreas(impl))
	}

	pcfg := pipelineConfig(cfg)
	p, err := core.New(deps, pcfg)
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
			if ev.Data.Audio > 0 {
				logf("turn %d: transcribed %.1f s of speech in %d ms", ev.Data.Turn, ev.Data.Audio.Seconds(), ev.Data.Took.Milliseconds())
			}
			if cfgFile.Current().Bool("history.transcripts") {
				if _, err := st.AddHistory(bctx, "transcript", ev.Data.Text); err != nil {
					logf("history: %v", err)
				}
			}
		}
	})
	// Timings of each dictation, for the log (never the text).
	inserts := bus.Subscribe(b, core.Inserted, 64)
	defer inserts.Close()
	bg.Go(func() {
		for ev := range inserts.C() {
			d := ev.Data
			line := fmt.Sprintf("turn %d: %s %d characters in %d ms", d.Turn, d.Method, len([]rune(d.Text)), d.Took.Milliseconds())
			if d.Latency > 0 {
				line += fmt.Sprintf("; key up to text %d ms", d.Latency.Milliseconds())
			}
			logf("%s", line)
		}
	})
	failures := bus.Subscribe(b, core.Failed, 64)
	defer failures.Close()
	bg.Go(func() {
		for ev := range failures.C() {
			logf("turn %d: %s", ev.Data.Turn, ev.Data.Err)
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

	// Get the permission to type and paste, and the session that does it,
	// now rather than at the first dictation (ADR-0030).
	if is, ok := deps.Platform.Input().(platform.InputSetup); ok && deps.VAD != nil {
		bg.Go(func() {
			if err := is.PrepareInsert(bctx); err != nil && bctx.Err() == nil {
				logf("text insertion not ready yet (it is tried again at the first dictation): %v", err)
			}
		})
	}

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
	api.Register(srv, api.Deps{Pipeline: p, Bus: b, Config: cfgFile, STT: deps.STT, VAD: deps.VAD, Models: models.Status, Info: api.Info{
		Version: version, Started: started, DevFakes: o.devFakes, Implementations: impl,
		Socket: o.socket, ConfigPath: o.configPath, Hotkeys: hotkeyInfo(pcfg),
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
// Speech-to-text "auto" (the default) is resolved by planSTT. It returns the
// pipeline dependencies (without the bus) and, per area, the name of what it
// chose ("" for none).
func wire(cfg *config.Config, o daemonOptions, logf func(string, ...any)) (d core.Deps, impl map[string]string, rl reloaders, err error) {
	impl = map[string]string{}
	devFakes, fakesOnly := o.devFakes || o.fakesOnly, o.fakesOnly
	name := func(want string) string {
		if fakesOnly {
			return fakeName
		}
		return want
	}
	speechName := name
	if o.realSpeech {
		speechName = func(want string) string { return want }
	}
	dirs := speechDirs{models: o.modelsDir, cache: o.cacheDir}
	if dirs.models == "" {
		dirs.models = modelsDir(o.dataDir)
	}
	if dirs.cache == "" {
		dirs.cache = defaultPaths().Cache
	}
	hw := o.hardware
	if hw == nil {
		hw = accel.Detect
	}
	defer func() {
		if err != nil {
			closeIfCloser(d.STT)
			closeIfCloser(d.VAD)
			closeIfCloser(d.Wake)
		}
	}()

	// Only the speech engines take options from config so far; every other
	// factory gets none.
	if d.Platform, impl["platform"], err = pick(platform.Platforms, name(runtime.GOOS), nil, devFakes); err != nil {
		return d, nil, rl, err
	}

	// Speech-to-text: "auto" and the native engines go through planSTT.
	sttWhat := "speech-to-text engine " + cfg.String("speech.stt.engine")
	plan := sttPlan{name: fakeName}
	if speechName("") != fakeName {
		if p, perr := planSTT(cfg, dirs, hw); perr != nil {
			sttWhat = "speech-to-text engine (" + perr.Error() + ")"
			plan = sttPlan{}
		} else {
			plan = p
			logf("speech-to-text: %s", describePlan(plan))
		}
	}
	if d.STT, impl["stt"], err = pick(speech.STTs, plan.name, plan.opts, devFakes); err != nil {
		return d, nil, rl, fmt.Errorf("%w\n(for development, run with --dev-fakes to use fakes)", err)
	}
	// The VAD is optional: without it the microphone is not used.
	vadName := speechName(defaultVAD)
	vadOpts := vadOptions(vadName, dirs, cfg.Float("speech.vad.min_silence"))
	if d.VAD, impl["vad"], err = pick(speech.VADs, vadName, vadOpts, devFakes); err != nil {
		logf("VAD unavailable: %v", err)
		d.VAD, impl["vad"], err = nil, "", nil
	}

	rl.stt = reloader(speech.STTs, impl["stt"], plan.opts)
	rl.vad = reloader(speech.VADs, impl["vad"], vadOpts)

	if cfg.Bool("input.wake_word") {
		// Like the VAD, the wake word is optional: without its model the
		// hotkeys still work.
		if d.Wake, impl["wake"], err = pick(speech.WakeWords, name(defaultWake), wakeOptions(name(defaultWake), dirs, cfg), devFakes); err != nil {
			logf("wake word unavailable: %v", err)
			d.Wake, impl["wake"], err = nil, "", nil
		}
	} else {
		impl["wake"] = ""
	}
	if d.TTS, impl["tts"], err = pick(speech.TTSs, name(cfg.String("speech.tts.engine")), nil, devFakes); err != nil {
		return d, nil, rl, err
	}
	rl.tts = reloader(speech.TTSs, impl["tts"], nil)
	if d.LLM, impl["llm"], err = pick(llm.Providers, name(cfg.String("ai.provider")), nil, devFakes); err != nil {
		return d, nil, rl, err
	}
	if cfg.String("ai.provider") == claude.Name && (!devFakes || impl["llm"] != fakeName) {
		d.LLM, impl["llm"] = newClaude(cfg, d.Platform), claude.Name
	}
	if d.Worker, impl["worker"], err = pick(jobs.Workers, name(cfg.String("jobs.worker")), nil, devFakes); err != nil {
		return d, nil, rl, err
	}

	// The pipeline cannot run without a platform and speech-to-text.
	for _, area := range []struct {
		key, what string
		names     []string
	}{
		{"platform", "platform " + runtime.GOOS, platform.Platforms.Names()},
		{"stt", sttWhat, speech.STTs.Names()},
	} {
		if impl[area.key] == "" {
			return d, nil, rl, fmt.Errorf("this build has no %s (registered: %v); "+
				"for development, run with --dev-fakes to use fakes", area.what, area.names)
		}
	}
	return d, impl, rl, nil
}

// pick builds the implementation registered as want, or the fake when want
// is not registered and devFakes is set. It returns the zero T and "" when
// neither is available.
func pick[T any](reg *registry.Registry[T], want string, opts registry.Config, devFakes bool) (T, string, error) {
	name := ""
	switch {
	case want != "" && reg.Has(want):
		name = want
	case devFakes && reg.Has(fakeName):
		name, opts = fakeName, nil
	default:
		var zero T
		return zero, "", nil
	}
	v, err := reg.New(name, opts)
	if err != nil {
		var zero T
		return zero, "", err
	}
	return v, name, nil
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
		DictateMode:   core.TalkMode(cfg.String("input.dictate.mode")),
		CommandHotkey: cfg.String("input.command.key"),
		CommandMode:   core.TalkMode(cfg.String("input.command.mode")),
		Mic:           mic,
		Insert:        dictation.InsertMode(cfg.String("dictation.insert")),
		Language:      cfg.String("user.language"),
		NoCommands:    cfg.String("dictation.commands") == "off",
		NoTerminal:    !cfg.Bool("dictation.terminal_mode"),
	}
}

// hotkeyInfo describes the push-to-talk keys for status.get.
func hotkeyInfo(c core.Config) []api.HotkeyInfo {
	var out []api.HotkeyInfo
	for _, k := range []struct {
		action, key string
		mode        core.TalkMode
	}{{"dictate", c.DictateHotkey, c.DictateMode}, {"command", c.CommandHotkey, c.CommandMode}} {
		if k.key == "" {
			continue
		}
		mode := k.mode
		if mode == "" {
			mode = core.HoldToTalk
		}
		out = append(out, api.HotkeyInfo{Action: k.action, Key: k.key, Mode: string(mode)})
	}
	return out
}
