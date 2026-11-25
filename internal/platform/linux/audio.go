//go:build linux

package linux

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/platform"
)

// Audio is the PulseAudio and PipeWire audio backend. It drives the standard
// `pactl`, `parec` and `pacat` tools (package pulseaudio-utils; they also talk
// to PipeWire through pipewire-pulse), so it needs no cgo and no extra Go
// dependency. See ADR-0026.
type Audio struct {
	run runFunc
	// Tool names; tests point these at fakes.
	pactl, parec, pacat string

	capturing atomic.Int32
}

var _ platform.Audio = (*Audio)(nil)

func newAudio(run runFunc) *Audio {
	return &Audio{run: run, pactl: "pactl", parec: "parec", pacat: "pacat"}
}

// bufferMs is the server-side buffer asked of parec and pacat. Small buffers
// keep capture and playback latency low; the server may round it up.
const bufferMs = 20

// Capturing returns how many microphones are open right now, for the tray
// status (design §29): the daemon shows MicOff when it is zero.
func (a *Audio) Capturing() int { return int(a.capturing.Load()) }

type pactlDevice struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (a *Audio) list(ctx context.Context, kind string) ([]pactlDevice, error) {
	res, err := a.run(ctx, runSpec{Name: a.pactl, Args: []string{"--format=json", "list", kind}, Capture: true})
	if err != nil {
		return nil, a.toolErr("audio", err)
	}
	var devs []pactlDevice
	if err := json.Unmarshal([]byte(res.Stdout), &devs); err != nil {
		return nil, fmt.Errorf("linux audio: parse pactl output: %w", err)
	}
	return devs, nil
}

func (a *Audio) defaultName(ctx context.Context, what string) string {
	res, err := a.run(ctx, runSpec{Name: a.pactl, Args: []string{"get-default-" + what}, Capture: true})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

func (a *Audio) toolErr(what string, err error) error {
	if errors.Is(err, errNoTool) {
		return unsupportedTool(what, "install pulseaudio-utils (pactl, parec, pacat)", err)
	}
	return fmt.Errorf("linux %s: %w", what, err)
}

// startErr wraps a failure to start tool.
func (a *Audio) startErr(what, tool string, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		err = errNoTool
	}
	return a.toolErr(what, fmt.Errorf("%s: %w", tool, err))
}

// Inputs lists microphones. Monitor sources (what a speaker is playing) are
// left out; Capture still accepts them by name.
func (a *Audio) Inputs(ctx context.Context) ([]platform.Device, error) {
	devs, err := a.list(ctx, "sources")
	if err != nil {
		return nil, err
	}
	def := a.defaultName(ctx, "source")
	var out []platform.Device
	for _, d := range devs {
		if strings.HasSuffix(d.Name, ".monitor") {
			continue
		}
		out = append(out, platform.Device{ID: d.Name, Name: d.Description, Default: d.Name == def})
	}
	return out, nil
}

// Outputs lists speakers.
func (a *Audio) Outputs(ctx context.Context) ([]platform.Device, error) {
	devs, err := a.list(ctx, "sinks")
	if err != nil {
		return nil, err
	}
	def := a.defaultName(ctx, "sink")
	out := make([]platform.Device, 0, len(devs))
	for _, d := range devs {
		out = append(out, platform.Device{ID: d.Name, Name: d.Description, Default: d.Name == def})
	}
	return out, nil
}

// exists reports whether a source or sink called name exists.
func (a *Audio) exists(ctx context.Context, kind, name string) error {
	devs, err := a.list(ctx, kind)
	if err != nil {
		return err
	}
	for _, d := range devs {
		if d.Name == name {
			return nil
		}
	}
	return fmt.Errorf("audio device %q: %w", name, platform.ErrNotFound)
}

func toolArgs(kind, device string, f audio.Format) []string {
	args := []string{
		"--raw",
		"--format=float32le",
		fmt.Sprintf("--rate=%d", f.SampleRate),
		fmt.Sprintf("--channels=%d", f.Channels),
		fmt.Sprintf("--latency-msec=%d", bufferMs),
		"--client-name=footman",
		"--stream-name=footman-" + kind,
	}
	if device != "" {
		args = append(args, "--device="+device)
	}
	return args
}

// stopWith makes cmd end with SIGTERM when ctx is done, and SIGKILL if it has
// not exited a second later.
func stopWith(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
}

// Capture opens source device ("" for the default) and sends frames in format
// f until ctx is done, when it stops the recorder and closes the channel. The
// channel is also closed if the device goes away. Frames are whatever has
// arrived from the server, typically about 20 ms, so a frame is never held
// back waiting for more audio.
func (a *Audio) Capture(ctx context.Context, device string, f audio.Format) (<-chan audio.Frame, error) {
	if !f.Valid() {
		return nil, fmt.Errorf("linux audio: invalid format %+v", f)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if device != "" {
		if err := a.exists(ctx, "sources", device); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, a.parec, toolArgs("capture", device, f)...)
	stopWith(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("linux audio: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, a.startErr("audio capture", a.parec, err)
	}
	a.capturing.Add(1)

	ch := make(chan audio.Frame, 64)
	go func() {
		defer close(ch)
		defer a.capturing.Add(-1)
		defer func() { _ = cmd.Wait() }()
		readFrames(ctx, out, f, ch)
	}()
	return ch, nil
}

// readFrames turns the recorder's float32le bytes into frames until EOF or
// ctx is done. Whole samples are sent as soon as they arrive.
func readFrames(ctx context.Context, r io.Reader, f audio.Format, ch chan<- audio.Frame) {
	unit := 4 * f.Channels
	buf := make([]byte, 16384)
	pending := 0 // bytes of a partial sample held over from the last read
	var offset time.Duration
	for {
		n, err := r.Read(buf[pending:])
		n += pending
		whole := n - n%unit
		if whole > 0 {
			samples := make([]float32, whole/4)
			for i := range samples {
				samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
			}
			fr := audio.Frame{Format: f, Samples: samples, Offset: offset}
			offset += fr.Duration()
			select {
			case ch <- fr:
			case <-ctx.Done():
				return
			}
		}
		pending = copy(buf, buf[whole:n])
		if err != nil {
			return
		}
	}
}

// Play plays s on sink device ("" for the default) and returns when the audio
// has finished playing, failed or ctx is done. It closes s.
func (a *Audio) Play(ctx context.Context, device string, s audio.Stream) error {
	defer func() { _ = s.Close() }()
	f := s.Format()
	if !f.Valid() {
		return fmt.Errorf("linux audio: invalid format %+v", f)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if device != "" {
		if err := a.exists(ctx, "sinks", device); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, a.pacat, append([]string{"--playback"}, toolArgs("playback", device, f)...)...)
	stopWith(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("linux audio: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return a.startErr("audio playback", a.pacat, err)
	}
	sendErr := pump(ctx, s, in) // unbuffered: each frame goes straight to pacat
	_ = in.Close()              // EOF makes pacat drain what it has and exit
	if sendErr != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM) // stop now instead of finishing
	}
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case sendErr != nil && !errors.Is(sendErr, syscall.EPIPE):
		return sendErr
	case waitErr != nil:
		return fmt.Errorf("linux audio playback: %w", waitErr)
	}
	return nil
}

// pump copies s to w as float32le until EOF, an error or ctx is done.
func pump(ctx context.Context, s audio.Stream, w io.Writer) error {
	var b []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fr, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		b = b[:0]
		for _, v := range fr.Samples {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
}

// DeviceChange reports a microphone or speaker appearing or disappearing, or
// the default changing. It is a hint to list devices again.
type DeviceChange struct {
	// Kind is "source", "sink" or "server" (the default device changed).
	Kind string
	// Action is "new", "remove" or "change".
	Action string
}

// Watch sends device changes until ctx is done, when it closes the channel.
// The channel is also closed if the audio server goes away.
func (a *Audio) Watch(ctx context.Context) (<-chan DeviceChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, a.pactl, "subscribe")
	stopWith(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("linux audio: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, a.startErr("audio watch", a.pactl, err)
	}
	ch := make(chan DeviceChange, 16)
	go func() {
		defer close(ch)
		defer func() { _ = cmd.Wait() }()
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			c, ok := parseSubscribe(sc.Text())
			if !ok {
				continue
			}
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// parseSubscribe reads a `pactl subscribe` line such as
// "Event 'new' on source #52". Volume and state changes on a device are not
// device changes, so only new and remove are kept for sources and sinks.
func parseSubscribe(line string) (DeviceChange, bool) {
	// Fields: Event 'new' on source #52
	w := strings.Fields(line)
	if len(w) < 4 || w[0] != "Event" || w[2] != "on" {
		return DeviceChange{}, false
	}
	action, kind := strings.Trim(w[1], "'"), w[3]
	switch {
	case (kind == "source" || kind == "sink") && (action == "new" || action == "remove"):
	case kind == "server" && action == "change":
	default:
		return DeviceChange{}, false
	}
	return DeviceChange{Kind: kind, Action: action}, true
}
