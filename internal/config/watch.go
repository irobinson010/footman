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
	last := stamp(path)
	var changedAt time.Time
	t := time.NewTicker(opts.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s := stamp(path); s != last {
			last = s
			changedAt = time.Now()
			continue
		}
		if !changedAt.IsZero() && time.Since(changedAt) >= opts.Debounce {
			changedAt = time.Time{}
			cfg, err := Load(path)
			onReload(cfg, err)
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
