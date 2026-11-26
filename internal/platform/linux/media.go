//go:build linux

package linux

import (
	"context"
	"fmt"
	"strings"

	"github.com/irobinson010/footman/internal/platform"
)

var _ platform.MediaControl = (*Platform)(nil)

const (
	mprisPrefix = "org.mpris.MediaPlayer2."
	mprisPath   = "/org/mpris/MediaPlayer2"
	mprisPlayer = "org.mpris.MediaPlayer2.Player"
)

var mprisMethods = map[platform.MediaCommand]string{
	platform.MediaPlay: "Play", platform.MediaPause: "Pause",
	platform.MediaNext: "Next", platform.MediaPrevious: "Previous",
}

// Media sends cmd to the MPRIS player that is playing, or else the first one.
func (p *Platform) Media(ctx context.Context, cmd platform.MediaCommand) error {
	method, ok := mprisMethods[cmd]
	if !ok {
		return fmt.Errorf("linux media: unknown command %q", cmd)
	}
	conn, err := p.bus.get()
	if err != nil {
		return fmt.Errorf("linux media: %w", err)
	}
	var names []string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return fmt.Errorf("linux media: %w", err)
	}
	target := ""
	for _, n := range names {
		if !strings.HasPrefix(n, mprisPrefix) {
			continue
		}
		if target == "" {
			target = n
		}
		v, err := conn.Object(n, mprisPath).GetProperty(mprisPlayer + ".PlaybackStatus")
		if err == nil && v.Value() == "Playing" {
			target = n
			break
		}
	}
	if target == "" {
		return fmt.Errorf("linux media: no media player running: %w", platform.ErrNotFound)
	}
	if err := conn.Object(target, mprisPath).CallWithContext(ctx, mprisPlayer+"."+method, 0).Err; err != nil {
		return fmt.Errorf("linux media: %s: %w", target, err)
	}
	return nil
}
