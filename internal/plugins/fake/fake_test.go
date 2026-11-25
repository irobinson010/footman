package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/irobinson010/footman/internal/plugins"
	"github.com/irobinson010/footman/internal/plugins/fake"
	"github.com/irobinson010/footman/internal/registry"
)

func TestTool(t *testing.T) {
	tool, err := plugins.Tools.New(fake.Name, registry.Map{
		"name": "send_email", "permission": "outward", "results": []string{"sent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := tool.Spec(); s.Name != "send_email" || s.Permission != plugins.Outward {
		t.Fatalf("Spec = %+v", s)
	}
	tool.(*fake.Tool).Queue(plugins.Result{Text: "<html>", Untrusted: true})

	ctx := context.Background()
	r, err := tool.Run(ctx, json.RawMessage(`{"to":"a"}`))
	if err != nil || r.Text != "sent" {
		t.Fatalf("Run = %+v, %v", r, err)
	}
	r, err = tool.Run(ctx, json.RawMessage(`{"to":"b"}`))
	if err != nil || !r.Untrusted {
		t.Fatalf("Run = %+v, %v", r, err)
	}
	if _, err := tool.Run(ctx, nil); !errors.Is(err, fake.ErrNoResult) {
		t.Fatalf("empty queue err = %v", err)
	}
	if c := tool.(*fake.Tool).Calls(); len(c) != 3 || string(c[1]) != `{"to":"b"}` {
		t.Fatalf("Calls = %s", c)
	}

	if _, err := plugins.Tools.New(fake.Name, registry.Map{"permission": "admin"}); err == nil {
		t.Fatal("bad permission accepted")
	}
}
