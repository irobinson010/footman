package plugins_test

import (
	"encoding/json"
	"testing"

	"github.com/irobinson010/footman/internal/plugins"
)

func TestPermission(t *testing.T) {
	for _, p := range []plugins.Permission{plugins.Read, plugins.Act, plugins.Destructive, plugins.Outward} {
		got, err := plugins.ParsePermission(p.String())
		if err != nil || got != p {
			t.Fatalf("round trip %v: %v, %v", p, got, err)
		}
	}
	if _, err := plugins.ParsePermission("admin"); err == nil {
		t.Fatal("unknown permission accepted")
	}
	var zero plugins.Permission
	if zero.Valid() {
		t.Fatal("zero permission is valid")
	}

	b, err := json.Marshal(plugins.ToolSpec{Name: "x", Permission: plugins.Outward})
	if err != nil {
		t.Fatal(err)
	}
	var s plugins.ToolSpec
	if err := json.Unmarshal(b, &s); err != nil || s.Permission != plugins.Outward {
		t.Fatalf("json round trip = %+v, %v (%s)", s, err, b)
	}
	if err := (plugins.ToolSpec{Name: "x"}).Validate(); err == nil {
		t.Fatal("spec without permission validated")
	}
}
