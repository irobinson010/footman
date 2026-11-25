package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/registry"
)

func TestProvider(t *testing.T) {
	p, err := llm.Providers.New(fake.Name, registry.Map{"replies": []string{"Very good, sir."}})
	if err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCall{ID: "1", Name: "open_url", Args: json.RawMessage(`{"url":"https://youtube.com"}`)}
	p.(*fake.Provider).Queue(fake.Reply{Text: "Opening.", ToolCalls: []llm.ToolCall{call}})
	if !p.Capabilities().Tools {
		t.Fatal("fake should report tool support")
	}

	ctx := context.Background()
	req := llm.Request{System: "You are Ashford.", Messages: []llm.Message{{Role: llm.User, Text: "hello"}}}

	// First reply: streamed word by word.
	s, err := p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	for {
		e, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == llm.TextDelta {
			deltas = append(deltas, e.Text)
		}
	}
	_ = s.Close()
	if len(deltas) != 3 || deltas[0] != "Very " {
		t.Fatalf("deltas = %q", deltas)
	}

	// Second reply: a tool call, assembled with Collect.
	s, err = p.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	r, err := llm.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Text != "Opening." || r.Stop != llm.StopToolUse || len(r.Message.ToolCalls) != 1 || r.Message.ToolCalls[0].Name != "open_url" {
		t.Fatalf("reply = %+v", r)
	}

	if _, err := p.Chat(ctx, req); !errors.Is(err, fake.ErrNoReply) {
		t.Fatalf("empty queue err = %v", err)
	}
	if n := len(p.(*fake.Provider).Requests()); n != 3 {
		t.Fatalf("recorded %d requests, want 3", n)
	}
}

func TestStreamCancel(t *testing.T) {
	p, _ := llm.Providers.New(fake.Name, registry.Map{"replies": []string{"a b c"}})
	ctx, cancel := context.WithCancel(context.Background())
	s, err := p.Chat(ctx, llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	cancel()
	if _, err := s.Recv(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv after cancel = %v", err)
	}
}
