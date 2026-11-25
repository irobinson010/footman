//go:build linux && integration

package linux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealInsert is the owner's final check on the real desktop (README.md,
// "Real-app check"). It types and pastes into whatever window has focus, so
// it only runs when FOOTMAN_REAL_INPUT=1 is set.
//
//	FOOTMAN_REAL_INPUT=1 go test -tags integration -v -count=1 -timeout 30m \
//	  ./internal/platform/linux/ -run TestRealInsert
//
// For each app in FOOTMAN_REAL_APPS (default Konsole, Brave, Firefox, VS
// Code) it counts down so you can click into a text field, then types an
// ASCII line and pastes a Unicode line after it. Neither has a newline, so
// nothing is sent or run.
func TestRealInsert(t *testing.T) {
	if os.Getenv("FOOTMAN_REAL_INPUT") != "1" {
		t.Skip("types into the real desktop; set FOOTMAN_REAL_INPUT=1 to run it")
	}
	p := realPlatform(t)
	in := p.input
	// Name the test binary as Footman for this run, so the portal dialog says
	// "Footman"; removed at the end.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	files, err := in.SetupInput(context.Background(), exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, f := range files.Program {
			_ = os.Remove(f)
		}
	})
	time.Sleep(2 * time.Second) // let the portal notice the new .desktop file
	in.tokenFile = filepath.Join(t.TempDir(), "remote-desktop.token")
	in.remote.tokenFile = in.tokenFile

	ctx := context.Background()
	fmt.Println("Starting the RemoteDesktop session. Approve the \"Footman\" dialog if it appears.")
	if _, _, err := in.remote.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	dc, _, err := in.wayland(ctx)
	if err != nil || dc == nil {
		t.Fatalf("no data-control clipboard: %v", err)
	}
	km, group := in.layout(ctx)
	if km == nil {
		t.Log("no keymap from the compositor: auto mode would treat only ASCII as typeable")
	} else {
		t.Logf("keymap: %d layout group(s), active group %d", len(km.groups), group+1)
	}

	apps := []string{"Konsole", "Brave", "Firefox", "VS Code"}
	if v := os.Getenv("FOOTMAN_REAL_APPS"); v != "" {
		apps = strings.Split(v, ",")
	}
	const typed = "typed: The quick brown fox, 0123456789 (ok)."
	const pasted = " pasted: Café déjà vu — naïve “quotes” ½ Straße αβγ Привет 日本語 👋 ✓"
	for i, app := range apps {
		fmt.Printf("\n[%d/%d] Click into a text field in %s (a shell prompt in Konsole; an editor tab in VS Code).\n", i+1, len(apps), strings.TrimSpace(app))
		for s := 8; s > 0; s-- {
			fmt.Printf("\r  inserting in %d s ", s)
			time.Sleep(time.Second)
		}
		fmt.Println()
		before, _ := dc.snapshot(ctx)
		w, werr := in.windows.Active(ctx)
		if err := in.Type(ctx, typed); err != nil {
			t.Fatalf("%s: Type: %v", app, err)
		}
		if err := in.Paste(ctx, pasted); err != nil {
			t.Fatalf("%s: Paste: %v", app, err)
		}
		after, _ := dc.snapshot(ctx)
		keys := "Ctrl+V"
		if werr == nil && isTerminal(w.App) {
			keys = "Ctrl+Shift+V"
		}
		restored := before.String() == after.String()
		fmt.Printf("  focused app %q, pasted with %s, previous clipboard restored: %v\n", w.App, keys, restored)
		fmt.Printf("  the field should now end with exactly:\n  %s%s\n", typed, pasted)
		if !restored {
			t.Errorf("%s: clipboard before %s, after %s", app, before, after)
		}
	}
	fmt.Println("\nNow open the clipboard history (Klipper, the tray's clipboard icon): the pasted line should not be listed.")
}
