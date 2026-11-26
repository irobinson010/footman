//go:build linux

package linux

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/irobinson010/footman/internal/platform"
)

// player is a fake MPRIS player.
type player struct {
	mu     sync.Mutex
	status string
	calls  []string
}

func (p *player) Play() *dbus.Error     { return p.rec("Play") }
func (p *player) Pause() *dbus.Error    { return p.rec("Pause") }
func (p *player) Next() *dbus.Error     { return p.rec("Next") }
func (p *player) Previous() *dbus.Error { return p.rec("Previous") }
func (p *player) rec(s string) *dbus.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, s)
	return nil
}

func (p *player) export(t *testing.T, c *dbus.Conn) {
	t.Helper()
	if err := c.Export(p, mprisPath, mprisPlayer); err != nil {
		t.Fatal(err)
	}
	if err := c.Export(playerProps{p}, mprisPath, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
}

// playerProps answers Properties.Get for PlaybackStatus.
type playerProps struct{ p *player }

func (pp playerProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(pp.p.status), nil
}

func TestMedia(t *testing.T) {
	addr := startBus(t)
	idle, playing := &player{status: "Paused"}, &player{status: "Playing"}
	idle.export(t, serve(t, addr, mprisPrefix+"a_idle"))
	playing.export(t, serve(t, addr, mprisPrefix+"b_playing"))
	p := newTestPlatform(t, addr, defaultRun)
	ctx := context.Background()

	if err := p.Media(ctx, platform.MediaPause); err != nil {
		t.Fatal(err)
	}
	if len(idle.calls) != 0 || len(playing.calls) != 1 || playing.calls[0] != "Pause" {
		t.Errorf("idle %v, playing %v", idle.calls, playing.calls)
	}
	if err := p.Media(ctx, platform.MediaNext); err != nil {
		t.Fatal(err)
	}
	if got := playing.calls[len(playing.calls)-1]; got != "Next" {
		t.Errorf("last call %q", got)
	}
	if err := p.Media(ctx, "bogus"); err == nil {
		t.Error("bogus command accepted")
	}
}

func TestMediaNoPlayer(t *testing.T) {
	p := newTestPlatform(t, startBus(t), defaultRun)
	if err := p.Media(context.Background(), platform.MediaPlay); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("no player: %v", err)
	}
}
