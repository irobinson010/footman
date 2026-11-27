package main

import (
	"context"
	"slices"
	"time"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
)

// The "system" context item (ADR-0043): the daemon collects the machine's
// facts once at start and gives the pipeline their prompt block while the
// AI's context policy allows it, following config reloads.

// localProviders are the AI providers that run on this computer, whose
// requests follow context.local; every other provider follows
// context.cloud.
var localProviders = []string{"ollama"}

// aiContextKey is the context policy that applies to the configured AI
// provider: context.local for a local one, otherwise context.cloud.
func aiContextKey(cfg *config.Config) string {
	if slices.Contains(localProviders, cfg.String("ai.provider")) {
		return "context.local"
	}
	return "context.cloud"
}

// machineFacts collects the computer's facts from the platform, or the
// portable basics when it cannot tell.
func machineFacts(ctx context.Context, plat platform.Platform, logf func(string, ...any)) platform.SystemFacts {
	if si, ok := plat.(platform.SystemInfo); ok {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		f, err := si.Facts(ctx)
		if err == nil && f.OS != "" {
			return f
		}
		if err != nil {
			logf("system facts: %v", err)
		}
	}
	return platform.BasicFacts()
}

// systemContext is the pipeline's system block for cfg: the facts, and
// user.location when set, while the AI's context policy includes "system",
// else "".
func systemContext(cfg *config.Config, facts platform.SystemFacts) string {
	if !config.ContextAllows(cfg.Strings(aiContextKey(cfg)), "system") {
		return ""
	}
	return core.SystemBlockAt(facts, cfg.String("user.location"))
}
