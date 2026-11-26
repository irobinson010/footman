//go:build unix

package main

import (
	"os"
	"strings"
	"testing"

	platformfake "github.com/irobinson010/footman/internal/platform/fake"
)

// "Switch to Klaus" sets personas.default in the config file and answers as
// Klaus; the reply is shown under his name (a typed command is not spoken).
func TestDaemonSwitchPersonaByVoice(t *testing.T) {
	d := startDaemon(t)
	if code, _, errOut := d.cli(t, "say", "switch", "to", "Klaus"); code != 0 {
		t.Fatalf("say = %d %s", code, errOut)
	}
	raw, err := os.ReadFile(d.config)
	if err != nil || !strings.Contains(string(raw), `default = "klaus"`) {
		t.Fatalf("config after the switch (%v):\n%s", err, raw)
	}
	shown := d.platform.Notifier().(*platformfake.Notifier).Shown()
	if len(shown) == 0 || shown[len(shown)-1].Title != "Klaus" || !strings.HasPrefix(shown[len(shown)-1].Body, "Klaus here.") {
		t.Errorf("notifications %+v", shown)
	}
	// Again: already there, nothing changes.
	if code, _, _ := d.cli(t, "say", "use", "klaus"); code != 0 {
		t.Fatal("second switch failed")
	}
	shown = d.platform.Notifier().(*platformfake.Notifier).Shown()
	if got := shown[len(shown)-1].Body; got != "Klaus is already here." {
		t.Errorf("second switch said %q", got)
	}
	d.log.waitFor(t, "persona: Klaus")
	// Klaus took over Ashford's place as the listening persona.
	if raw, err := os.ReadFile(d.config); err != nil || !strings.Contains(string(raw), `listening = ["klaus"]`) {
		t.Errorf("config after the switch: %s %v", raw, err)
	}
}
