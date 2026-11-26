package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/speech"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/speechsetup"
)

// setupSpeech is the speech step of `footman setup` (ADR-0028): models, GPU
// warm-up and benchmark. Download progress goes to progress (stderr); the
// step lines go through install.SpeechEnv.Step.
func setupSpeech(progress io.Writer) func(context.Context, install.SpeechEnv) error {
	return func(ctx context.Context, se install.SpeechEnv) error {
		dirs := speechDirs{models: modelsDir(se.DataDir), cache: se.CacheDir}
		return runSpeechSetup(ctx, se, dirs, accel.Detect(), accel.Engines(), progress)
	}
}

func runSpeechSetup(ctx context.Context, se install.SpeechEnv, dirs speechDirs, hw accel.Hardware, engines map[string][]string, progress io.Writer) error {
	// The default persona's voice is fetched too (download only; it runs on
	// the CPU).
	var voices []string
	if cfg, err := config.Load(se.ConfigPath); err == nil {
		voices = personaVoiceModels(cfg, se.DataDir, func(f string, a ...any) { se.Step("persona      "+f, a...) })
	}
	env := speechsetup.Env{
		Voices:     voices,
		ConfigPath: se.ConfigPath,
		ModelsDir:  dirs.models,
		Hardware:   hw,
		Engines:    engines,
		Step:       se.Step,
		Progress:   downloadProgress(progress),
		Record: func(path, id string, isDir bool) error {
			if isDir {
				return se.Record(install.Entry{Kind: install.KindDir, Group: install.GroupModels, Path: path})
			}
			return se.Record(install.Entry{Kind: install.KindModel, Group: install.GroupModels, Path: path, Name: id})
		},
		Open: func(_ context.Context, c accel.Choice) (speech.STT, error) {
			cfg, err := config.Load(se.ConfigPath)
			if err != nil {
				return nil, err
			}
			// The shader cache goes on the receipt before the engine can
			// fill it, so uninstall removes it even if the warm-up is
			// interrupted (ADR-0023).
			if c.Accelerator == accel.Vulkan {
				if err := se.Record(install.Entry{Kind: install.KindDir, Group: install.GroupCache, Path: dirs.cache}); err != nil {
					return nil, err
				}
			}
			stt, err := speech.STTs.New(c.Engine, sttOptions(c, cfg.String("user.language"), dirs))
			if err != nil {
				return nil, err
			}
			if s, ok := stt.(shaderCached); ok && s.ShaderCache() != "" {
				if err := se.Record(install.Entry{Kind: install.KindTree, Group: install.GroupCache, Path: s.ShaderCache()}); err != nil {
					closeIfCloser(stt)
					return nil, err
				}
			}
			return stt, nil
		},
	}
	_, err := speechsetup.Run(ctx, env)
	return err
}

// downloadProgress prints a model download's progress on one updating line,
// at most once a second.
func downloadProgress(w io.Writer) func(id string, done, total int64) {
	var cur string
	var last time.Time
	lastDone := int64(-1)
	return func(id string, done, total int64) {
		if id != cur {
			cur, last, lastDone = id, time.Time{}, -1
		}
		if done == lastDone || (time.Since(last) < time.Second && done < total) {
			return
		}
		last, lastDone = time.Now(), done
		_, _ = fmt.Fprintf(w, "\r%s: %3d%% of %.1f MB", id, 100*done/max(total, 1), float64(total)/(1<<20))
		if done >= total {
			_, _ = fmt.Fprintln(w)
		}
	}
}
