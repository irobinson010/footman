package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
	"github.com/irobinson010/footman/internal/permissions"
	"github.com/irobinson010/footman/internal/persona/clipbank"
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

	// The default persona gives the reply voice, the AI's system prompt and
	// the stock lines; the listening personas give the wake words (S3-07).
	personas := newPersonaWiring(o.dataDir, logf)
	current := personas.load(cfg)
	wakeWords := personaWakeWords(personas.cat, personas.userDir, cfg)
	deps, impl, reload, err := wire(cfg, o, wakeWords, logf)
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
	after := func() time.Duration { return unloadAfter(cfgFile.Current()) }
	models := manageSpeech(&deps, impl, reload, after)
	defer models.Close()
	// Text-to-speech: the persona's engine and voice, or the engine
	// speech.tts.engine names. Without one, replies are only shown.
	tts := newTTSEngines(reload.tts, idleOptions(after))
	models.addTTS(tts)
	engine, voice := ttsChoice(cfg, current)
	if err := tts.Use(engine, voice); err != nil {
		logf("text-to-speech unavailable, so replies are only shown (fix it and restart): %v", err)
	} else {
		deps.TTS, impl["tts"] = tts, tts.Current()
		personas.tts = tts
	}
	deps.Bus = b
	// The permission gate reads the policy live from the config (ADR-0038)
	// and asks the confirmation broker, which shows a notification with
	// Allow and Deny and answers `footman confirm` (ADR-0039). While it
	// waits, the persona says its confirm line if the reply is spoken.
	broker := &permissions.Broker{Bus: b, Logf: logf}
	defer broker.Close()
	gate := &permissions.Gate{Config: cfgFile.Current, Confirmer: speakingConfirmer{broker}}
	deps.Gate = gate
	// Settings by voice: tightening applies at once, loosening asks the
	// broker for a click.
	settings := &core.Settings{Store: cfgFile, Bus: b, Confirm: broker}
	deps.Tools = append(deps.Tools, core.SettingsTools(settings)...)
	if o.deps != nil {
		o.deps(&deps)
	}
	broker.Notifier = deps.Platform.Notifier()
	if o.devFakes {
		logf("WARNING: running with --dev-fakes; fakes stand in for: %v", fakedAreas(impl))
	}

	pcfg := pipelineConfig(cfg)
	// "Ashford, open YouTube" must lose "Ashford", as the spotter heard it.
	pcfg.WakeWords = routerWakeWords(wakeWords)
	pcfg.FastRules = append([]core.FastRule{
		core.SettingsRule(settings),
		core.PersonaRule(switcher{w: personas, settings: settings}),
	}, core.DefaultFastRules(nil)...)
	if pcfg.Mic == "" && cfg.Bool("audio.prefer_echo_cancel") {
		if devs, err := deps.Platform.Audio().Inputs(ctx); err == nil {
			if d, ok := echoCancelSource(devs); ok {
				logf("using echo-cancelling microphone %q", d.Name)
				pcfg.Mic = d.ID
			}
		}
	}
	// The clip bank (S3-03, ADR-0040) pre-renders the persona's short
	// lines with the CPU voice into <cache>/clips and plays the
	// acknowledgement from memory.
	bank := clipbank.New(clipbank.Options{
		Dir: clipsDir(o), TTS: deps.TTS,
		Log: slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	defer bank.Close()
	if err := install.RecordIn(o.dataDir, install.Entry{Kind: install.KindTree, Group: install.GroupCache, Path: clipsDir(o)}); err != nil {
		logf("could not record the clip cache in the install receipt: %v", err)
	}
	pcfg.Ack = bank
	p, err := core.New(deps, pcfg)
	if err != nil {
		return err
	}
	defer p.Close()
	// From here on the persona follows the config: the pipeline's voice,
	// prompt and lines, the wake words, and the clip bank.
	personas.OnChange(func(c PersonaChange) {
		bank.Set(clipbank.Target{Pack: c.Pack, Voice: c.Voice, Address: c.Address, Lang: c.Config.String("user.language")})
	})
	personas.pipeline = p
	if ws, ok := deps.Wake.(*wakeSwitch); ok {
		personas.wake = ws
	}
	personas.apply(cfg)

	var bg sync.WaitGroup
	defer bg.Wait()
	bctx, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBG()
	// The outcome of a clicked setting change is shown as a notification.
	settings.Told = func(text string) {
		if _, err := broker.Notifier.Notify(bctx, platform.Notification{Title: "Footman", Body: text}); err != nil && bctx.Err() == nil {
			logf("settings: %s (no notification: %v)", text, err)
		}
	}

	// The action log records every action from the bus; it stops (writing
	// what it has received) before the store closes.
	actionLog := &permissions.Log{Store: st, Config: cfgFile.Current, Logf: logf}
	defer actionLog.Watch(b)()
	// Retention: prune old history and action-log rows now and daily.
	bg.Go(func() { permissions.Retain(bctx, st, cfgFile.Current, permissions.PruneEvery, logf) })

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
	// The wake word and how each turn ended, for the journal (never the
	// text).
	wakes := bus.Subscribe(b, core.WakeHeard, 64)
	defer wakes.Close()
	bg.Go(func() {
		for ev := range wakes.C() {
			logf("wake word heard: %s", ev.Data.Keyword)
		}
	})
	rejected := bus.Subscribe(b, core.WakeRejected, 64)
	defer rejected.Close()
	bg.Go(func() {
		for ev := range rejected.C() {
			logf("%s", wakeRejectedLine(ev.Data))
		}
	})
	outcomes := bus.Subscribe(b, core.TurnDone, 64)
	defer outcomes.Close()
	bg.Go(func() {
		for ev := range outcomes.C() {
			logf("%s", outcomeLine(ev.Data))
		}
	})

	// Pick up edits to the config file (by hand, config.set or voice): the
	// reply policy and the persona apply at once; other pipeline settings
	// are read at start for now. The API sees the new values at once.
	bg.Go(func() {
		config.Watch(bctx, o.configPath, config.WatchOptions{}, func(c *config.Config, err error) {
			if err != nil {
				logf("config not reloaded: %v", err)
				return
			}
			old := cfgFile.Current()
			cfgFile.Replace(c)
			p.SetReplyPolicy(c.String("replies.speak"), c.String("replies.quiet_hours"))
			p.SetReplyNotify(c.Bool("replies.notify"))
			personas.apply(c)
			// Changes made through config.set or by voice are already in
			// old, so only hand edits show up here.
			permissions.PublishConfigChanges(b, old, c, core.ActorOwner, permissions.HowAuto, "config file edited")
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
	api.Register(srv, api.Deps{Pipeline: p, Bus: b, Config: cfgFile, Actions: st, Confirm: broker, STT: deps.STT, VAD: deps.VAD, Models: models.Status, Info: api.Info{
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
func wire(cfg *config.Config, o daemonOptions, wakeWords []string, logf func(string, ...any)) (d core.Deps, impl map[string]string, rl reloaders, err error) {
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
		// hotkeys still work. Its words change with the personas, so the
		// engine is behind a wakeSwitch that builds a new one then.
		var w speech.WakeWord
		if w, impl["wake"], err = pick(speech.WakeWords, name(defaultWake), wakeOptions(name(defaultWake), dirs, cfg, wakeWords), devFakes); err != nil {
			logf("wake word unavailable: %v", err)
			w, impl["wake"], err = nil, "", nil
		}
		if w != nil {
			wakeImpl := impl["wake"]
			d.Wake = &wakeSwitch{cur: w, words: wakeWords, build: func(words []string) (speech.WakeWord, error) {
				return speech.WakeWords.New(wakeImpl, wakeOptions(wakeImpl, dirs, cfg, words))
			}}
		}
	} else {
		impl["wake"] = ""
	}
	// Text-to-speech is built after wiring, for the persona's engine (see
	// ttsEngines); it is optional, as replies can be shown instead.
	rl.tts = func(engine string, v speech.Voice) (speech.TTS, string, error) {
		return pick(speech.TTSs, name(engine), ttsOptions(name(engine), dirs, v), devFakes)
	}
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
		AGC:           cfg.Bool("audio.agc"),
		AGCMaxGainDB:  cfg.Float("audio.agc_max_gain_db"),
		Insert:        dictation.InsertMode(cfg.String("dictation.insert")),
		Language:      cfg.String("user.language"),
		NoCommands:    cfg.String("dictation.commands") == "off",
		NoTerminal:    !cfg.Bool("dictation.terminal_mode"),
		MinCommand:    core.DefaultMinCommand,
		VerifyWake:    cfg.Bool("speech.wake.verify"),
		Quiet:         cfg.Bool("replies.quiet"),
		Speak:         cfg.String("replies.speak"),
		Notify:        cfg.Bool("replies.notify"),
		QuietHours:    cfg.String("replies.quiet_hours"),
	}
}

// clipsDir is where the clip bank caches its clips: <cache>/clips (in tests
// without a cache folder, <data>/cache/clips).
func clipsDir(o daemonOptions) string {
	cache := o.cacheDir
	if cache == "" {
		cache = filepath.Join(o.dataDir, "cache")
	}
	return filepath.Join(cache, "clips")
}

// speakingConfirmer is the permission broker with the persona's confirm
// line said (when the reply is spoken) as each confirmation is asked.
type speakingConfirmer struct{ *permissions.Broker }

func (c speakingConfirmer) Confirm(ctx context.Context, r permissions.Request) (permissions.Decision, error) {
	core.Confirming(ctx)
	return c.Broker.Confirm(ctx, r)
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

// wakeRejectedLine is the journal line for a wake the second stage dropped.
func wakeRejectedLine(r core.WakeRejection) string {
	return fmt.Sprintf("turn %d: wake word not confirmed by speech-to-text (%s; %.1f s checked in %d ms), dropped",
		r.Turn, r.Reason, r.Audio.Seconds(), r.Took.Milliseconds())
}

// outcomeLine is the journal line for how a turn ended: the trigger, the
// route, what handled a command, and the error, if any. Never the text.
func outcomeLine(o core.Outcome) string {
	line := fmt.Sprintf("turn %d: %s -> %s", o.Turn, o.Trigger, o.Route)
	if o.Handler != "" {
		line += " via " + o.Handler
	}
	if o.Err != "" {
		return line + ", failed: " + o.Err
	}
	return line + ", ok"
}
