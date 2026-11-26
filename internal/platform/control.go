package platform

import "context"

// VolumeControl is an optional extension of Platform for the system volume.
// The fast path checks for it with a type assertion.
type VolumeControl interface {
	// ChangeVolume raises or lowers the output volume by delta percentage
	// points.
	ChangeVolume(ctx context.Context, delta int) error
	// SetVolume sets the output volume to percent (0 to 100).
	SetVolume(ctx context.Context, percent int) error
	// SetMute mutes or unmutes the output.
	SetMute(ctx context.Context, muted bool) error
}

// MediaCommand is a media-player transport command.
type MediaCommand string

// Media commands.
const (
	MediaPlay     MediaCommand = "play"
	MediaPause    MediaCommand = "pause"
	MediaNext     MediaCommand = "next"
	MediaPrevious MediaCommand = "previous"
)

// MediaControl is an optional extension of Platform for the running media
// player (MPRIS on Linux). It returns an error wrapping ErrNotFound when no
// player is running.
type MediaControl interface {
	Media(ctx context.Context, cmd MediaCommand) error
}
