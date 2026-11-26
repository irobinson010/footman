//go:build linux

package linux

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/irobinson010/footman/internal/platform"
)

var _ platform.VolumeControl = (*Platform)(nil)

// ChangeVolume raises or lowers the default sink's volume by delta points.
func (p *Platform) ChangeVolume(ctx context.Context, delta int) error {
	return p.pactl(ctx, "set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%+d%%", delta))
}

// SetVolume sets the default sink's volume, clamped to 0 to 100.
func (p *Platform) SetVolume(ctx context.Context, percent int) error {
	percent = min(max(percent, 0), 100)
	return p.pactl(ctx, "set-sink-volume", "@DEFAULT_SINK@", strconv.Itoa(percent)+"%")
}

// SetMute mutes or unmutes the default sink.
func (p *Platform) SetMute(ctx context.Context, muted bool) error {
	v := "0"
	if muted {
		v = "1"
	}
	return p.pactl(ctx, "set-sink-mute", "@DEFAULT_SINK@", v)
}

func (p *Platform) pactl(ctx context.Context, args ...string) error {
	_, err := p.run(ctx, runSpec{Name: "pactl", Args: args})
	if errors.Is(err, errNoTool) {
		return unsupportedTool("volume", "install pactl (pulseaudio-utils or pipewire-pulse)", err)
	}
	if err != nil {
		return fmt.Errorf("linux volume: %w", err)
	}
	return nil
}
