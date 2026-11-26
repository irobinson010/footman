//go:build live

package claude_test

import (
	"context"
	"os"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
)

// TestLiveChat calls the real API. Run with -tags live and ANTHROPIC_API_KEY.
func TestLiveChat(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	p := claude.New(claude.Options{APIKey: func(context.Context) (string, error) { return key, nil }})
	s, err := p.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "Say hi in one word."}}, MaxTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	r, err := llm.Collect(s)
	if err != nil || r.Message.Text == "" {
		t.Fatalf("reply %+v, err %v", r, err)
	}
}
