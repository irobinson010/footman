package main

import (
	"context"
	"errors"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
	"github.com/irobinson010/footman/internal/platform"
)

// newClaude builds the Claude provider from config. The API key is resolved
// from the keychain reference at call time and never logged. Usage is
// tracked for the cost caps.
func newClaude(cfg *config.Config, plat platform.Platform) llm.Provider {
	const base = "ai.providers.claude."
	model := cfg.String(base + "model")
	p := claude.New(claude.Options{
		Model:   model,
		BaseURL: cfg.String(base + "url"),
		APIKey: func(ctx context.Context) (string, error) {
			ref, ok := cfg.Secret(base + "api_key")
			if !ok || ref.Name == "" {
				return "", errors.New("ai.providers.claude.api_key is not set (expected keychain:claude)")
			}
			return plat.Secrets().Get(ctx, ref.Name)
		},
	})
	if model == "" {
		model = claude.DefaultModel
	}
	return llm.Metered(p, llm.NewTracker(nil), model)
}
