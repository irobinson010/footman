package permissions

import (
	"context"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/store"
)

// Pruner is the part of the store retention uses.
type Pruner interface {
	Prune(ctx context.Context, historyOlderThan, actionLogOlderThan time.Duration) (store.Pruned, error)
}

// PruneEvery is how often the daemon prunes old history and action-log rows.
const PruneEvery = 24 * time.Hour

const day = 24 * time.Hour

// RetentionAges returns the ages store.Prune is given for cfg:
//   - history.retention_days: rows older than that many days go; 0 keeps
//     none (every existing row goes).
//   - permissions.action_log_retention_days: entries older than that many
//     days go; 0 keeps them forever.
func RetentionAges(cfg *config.Config) (history, actionLog time.Duration) {
	history = time.Duration(cfg.Int("history.retention_days")) * day
	if history <= 0 {
		history = time.Nanosecond
	}
	actionLog = time.Duration(cfg.Int("permissions.action_log_retention_days")) * day
	return history, actionLog
}

// Retain prunes the store at once and then every interval, with the ages
// read from the config each time, until ctx ends. Nil cfg means the
// defaults; nil logf drops reports.
func Retain(ctx context.Context, s Pruner, cfg func() *config.Config, every time.Duration, logf func(string, ...any)) {
	if every <= 0 {
		every = PruneEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		c := defaults
		if cfg != nil {
			if cc := cfg(); cc != nil {
				c = cc
			}
		}
		h, a := RetentionAges(c)
		p, err := s.Prune(ctx, h, a)
		if logf != nil {
			switch {
			case err != nil && ctx.Err() == nil:
				logf("retention: %v", err)
			case p.History > 0 || p.ActionLog > 0:
				logf("retention: removed %d history and %d action-log rows", p.History, p.ActionLog)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
