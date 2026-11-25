package config

import (
	"context"
	"os"
	"time"
)

// WatchOptions tunes Watch. Zero values give sensible defaults.
type WatchOptions struct {
	Poll     time.Duration // how often to stat the file (default 500ms)
	Debounce time.Duration // quiet time required after the last change (default 250ms)
}

// Watch calls onReload with the freshly loaded config (or the load error)
// after the file at path changes, until ctx is cancelled. Bursts of writes
// (editors that write twice, SetFile's rename) collapse into one call. The
// file is polled by modification time and size: that needs no dependency
// and works the same on Linux, macOS and Windows, and a half-second delay
// is fine for settings. A file that appears or disappears also triggers a
// reload; a missing file loads as defaults.
func Watch(ctx context.Context, path string, opts WatchOptions, onReload func(*Config, error)) {
	if opts.Poll <= 0 {
		opts.Poll = 500 * time.Millisecond
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 250 * time.Millisecond
	}
	t := time.NewTicker(opts.Poll)
	defer t.Stop()
	watch(ctx, path, opts.Debounce, onReload, watchEnv{
		tick:  t.C,
		now:   time.Now,
		stamp: stamp,
		load:  Load,
	})
}

// watchEnv holds the things Watch gets from the outside world, so tests can
// replace them and drive time explicitly.
type watchEnv struct {
	tick      <-chan time.Time
	now       func() time.Time
	stamp     func(path string) fileStamp
	load      func(path string) (*Config, error)
	afterTick func() // optional; called after each tick is fully handled
}

func watch(ctx context.Context, path string, debounce time.Duration, onReload func(*Config, error), env watchEnv) {
	last := env.stamp(path)
	var changedAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-env.tick:
		}
		if s := env.stamp(path); s != last {
			last = s
			changedAt = env.now()
		} else if !changedAt.IsZero() && env.now().Sub(changedAt) >= debounce {
			changedAt = time.Time{}
			cfg, err := env.load(path)
			onReload(cfg, err)
		}
		if env.afterTick != nil {
			env.afterTick()
		}
	}
}

type fileStamp struct {
	exists bool
	mod    time.Time
	size   int64
}

func stamp(path string) fileStamp {
	st, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{true, st.ModTime(), st.Size()}
}
