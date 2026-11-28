package llm_test

import (
	"context"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/fake"
	"github.com/irobinson010/footman/internal/registry"
)

func TestMeteredPassesWarmOn(t *testing.T) {
	p, err := fake.New(registry.Map{})
	if err != nil {
		t.Fatal(err)
	}
	m := llm.Metered(p, llm.NewTracker(nil), "m")
	w, ok := m.(interface{ Warm(context.Context) })
	if !ok {
		t.Fatal("a metered provider cannot be warmed")
	}
	w.Warm(context.Background())
	if p.Warms() != 1 {
		t.Errorf("warms = %d, want 1", p.Warms())
	}
}
