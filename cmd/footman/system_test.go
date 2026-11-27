package main

import (
	"strings"
	"testing"

	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/platform"
	pfake "github.com/irobinson010/footman/internal/platform/fake"
	"github.com/irobinson010/footman/internal/registry"
)

func TestSystemContextFollowsPolicy(t *testing.T) {
	facts := platform.SystemFacts{OS: "Linux", Distro: "Ubuntu 24.04 LTS"}
	for _, tc := range []struct {
		name, toml string
		want       bool
	}{
		{"default is on", ``, true},
		{"cloud turned off", "[context]\ncloud = []\n", false},
		{"cloud all", "[context]\ncloud = [\"all\"]\n", true},
		{"cloud other items only", "[context]\ncloud = [\"clipboard\"]\n", false},
		{"local off does not matter to claude", "[context]\nlocal = []\n", true},
		{"local provider follows local", "[ai]\nprovider = \"ollama\"\n[context]\nlocal = []\ncloud = [\"system\"]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tc.toml))
			if err != nil {
				t.Fatal(err)
			}
			got := systemContext(cfg, facts)
			if (got != "") != tc.want || (tc.want && !strings.Contains(got, "Ubuntu 24.04 LTS")) {
				t.Errorf("systemContext = %q, want on=%v", got, tc.want)
			}
		})
	}
}

func TestMachineFacts(t *testing.T) {
	p, err := pfake.New(registry.Map{"root": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	logf := func(string, ...any) {}
	// The fake knows nothing: the portable basics stand in.
	if f := machineFacts(t.Context(), p, logf); f.OS == "" || f.Arch == "" {
		t.Errorf("basic facts = %+v", f)
	}
	p.Sys = platform.SystemFacts{OS: "Linux", Distro: "Test OS 1"}
	if f := machineFacts(t.Context(), p, logf); f.Distro != "Test OS 1" {
		t.Errorf("facts = %+v, want the platform's", f)
	}
}
