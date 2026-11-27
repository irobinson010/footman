package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/config"
)

func TestDictationConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	dc := dictationConfig(config.Defaults())
	if want := filepath.Join(home, "Documents", "Footman notes"); dc.NotesDir != want {
		t.Errorf("NotesDir = %q, want %q", dc.NotesDir, want)
	}
	if want := []string{"stop dictation", "end dictation", "that's all"}; !slices.Equal(dc.StopPhrases, want) {
		t.Errorf("StopPhrases = %v, want %v", dc.StopPhrases, want)
	}
	if dc.IdleTimeout != time.Minute {
		t.Errorf("IdleTimeout = %v, want 1m", dc.IdleTimeout)
	}

	c, err := config.Parse([]byte("version = 1\n[dictation]\nidle_timeout = \"0s\"\nnotes_dir = \"/srv/notes\"\nstop_phrases = [\"over and out\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	dc = dictationConfig(c)
	if dc.IdleTimeout >= 0 || dc.NotesDir != "/srv/notes" || !slices.Equal(dc.StopPhrases, []string{"over and out"}) {
		t.Errorf("dictationConfig = %+v; want idle never (negative), /srv/notes and the one phrase", dc)
	}
}
