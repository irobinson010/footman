package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/audio"
	"github.com/irobinson010/footman/internal/config"
	"github.com/irobinson010/footman/internal/platform"
)

func init() {
	register(command{name: "mic", summary: "check the microphone level (`footman mic check`)", run: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		return cmdMic(ctx, args, stdout, stderr)
	}})
}

// micInteractive reports whether r is a terminal to ask questions on. A test
// seam.
var micInteractive = func(r io.Reader) bool { _, ok := terminalFD(r); return ok }

// Level thresholds for the check, in dBFS (ADR-0035).
const (
	micSilentPeak = -60.0 // peak below this: nothing is arriving
	micLowRMS     = -40.0 // speech RMS below this is too quiet for reliable wake detection
	micLowPeak    = -20.0
	micClipPeak   = -1.0
)

// cmdMic is `footman mic`.
func cmdMic(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		_, _ = fmt.Fprintln(stderr, "usage: footman mic check [--source ID] [--seconds N]")
		return 2
	}
	fs := newFlagSet("mic check", stderr)
	var source string
	var secs float64
	fs.StringVar(&source, "source", "", "input source to test (default: the configured microphone)")
	fs.Float64Var(&secs, "seconds", 5, "how long to listen")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || secs <= 0 || secs > 60 {
		_, _ = fmt.Fprintln(stderr, "footman mic check: unexpected argument, or --seconds not in (0, 60]")
		return 2
	}
	p, err := newPlatform()
	if err != nil {
		return fail(stderr, err)
	}
	o := micOptions{
		Audio:      p.Audio(),
		ConfigPath: filepath.Join(p.Paths().Config(), "config.toml"),
		Source:     source,
		Duration:   time.Duration(secs * float64(time.Second)),
		Ask:        micInteractive(stdin),
		In:         bufio.NewReader(stdin),
		Out:        stdout,
	}
	if err := micCheck(ctx, o); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// micOptions is what micCheck needs.
type micOptions struct {
	Audio platform.Audio
	// ConfigPath is the config file the chosen source is saved to.
	ConfigPath string
	// Source is the source to test; empty means the configured microphone.
	Source   string
	Duration time.Duration
	// Ask allows questions on In; otherwise the check just reports.
	Ask bool
	In  *bufio.Reader
	Out io.Writer
}

// micStats is what a capture measured.
type micStats struct {
	RMSDB, PeakDB float64
	Frames        int
}

// micCheck lists the input sources, listens to one for a few seconds (nothing
// is stored), reports the level and, when it can ask, lets the user try other
// sources and save one as input.microphone.
func micCheck(ctx context.Context, o micOptions) error {
	devs, err := o.Audio.Inputs(ctx)
	if err != nil {
		return fmt.Errorf("list input sources: %w", err)
	}
	if len(devs) == 0 {
		return errors.New("no input sources found")
	}
	current := "default"
	if cfg, err := config.Load(o.ConfigPath); err == nil {
		current = cfg.String("input.microphone")
	}
	cur := o.Source
	if cur == "" {
		cur = current
	}

	w := o.Out
	_, _ = fmt.Fprintln(w, "Input sources:")
	for i, d := range devs {
		var tags []string
		if d.Default {
			tags = append(tags, "system default")
		}
		if sourceMatches(d, current) {
			tags = append(tags, "configured")
		}
		if isEchoCancel(d) {
			tags = append(tags, "echo-cancelled")
		}
		t := ""
		if len(tags) > 0 {
			t = "  (" + strings.Join(tags, ", ") + ")"
		}
		_, _ = fmt.Fprintf(w, "  %d. %s%s\n", i+1, d.Name, t)
	}

	for {
		dev, ok := findSource(devs, cur)
		if !ok {
			return fmt.Errorf("input source %q: %w", cur, platform.ErrNotFound)
		}
		_, _ = fmt.Fprintf(w, "\nListening to %q for %s. Say something at normal volume.\n", dev.Name, o.Duration.Round(time.Second))
		st, err := measure(ctx, o.Audio, dev, o.Duration, w)
		if err != nil {
			return err
		}
		report(w, st, devs, dev)

		if !o.Ask {
			return nil
		}
		if !sourceMatches(dev, current) {
			if yes, err := askYes(o, fmt.Sprintf("Use %q as your microphone?", dev.Name)); err != nil {
				return err
			} else if yes {
				val := dev.ID
				if dev.Default || val == "" {
					val = "default"
				}
				if err := config.SetFile(o.ConfigPath, "input.microphone", val); err != nil {
					return fmt.Errorf("write %s: %w", o.ConfigPath, err)
				}
				current = val
				_, _ = fmt.Fprintf(w, "Saved input.microphone = %q in %s (restart the daemon to apply).\n", val, o.ConfigPath)
			}
		}
		_, _ = fmt.Fprint(w, "\nTry another source (number), or Enter to finish: ")
		line, err := o.In.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" || err != nil && !errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintln(w)
			return nil
		}
		n, convErr := strconv.Atoi(line)
		if convErr != nil || n < 1 || n > len(devs) {
			_, _ = fmt.Fprintf(w, "%q is not one of the numbers above.\n", line)
			return nil
		}
		cur = devs[n-1].ID
	}
}

func askYes(o micOptions, q string) (bool, error) {
	_, _ = fmt.Fprintf(o.Out, "%s [y/N] ", q)
	line, err := o.In.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	l := strings.ToLower(strings.TrimSpace(line))
	return l == "y" || l == "yes", nil
}

// sourceMatches reports whether setting s ("default", an ID or a name) names d.
func sourceMatches(d platform.Device, s string) bool {
	if s == "" || s == "default" {
		return d.Default
	}
	return d.ID == s || d.Name == s
}

func findSource(devs []platform.Device, s string) (platform.Device, bool) {
	for _, d := range devs {
		if sourceMatches(d, s) {
			return d, true
		}
	}
	return platform.Device{}, false
}

// isEchoCancel reports whether d looks like an echo-cancelling source, such
// as the one PipeWire's or PulseAudio's echo-cancel module creates.
func isEchoCancel(d platform.Device) bool {
	s := strings.ToLower(d.ID + " " + d.Name)
	if strings.Contains(s, ".monitor") || strings.Contains(s, "monitor of") {
		return false
	}
	return strings.Contains(s, "echo-cancel") || strings.Contains(s, "echo cancel") || strings.Contains(s, "echocancel")
}

// echoCancelSource returns the first echo-cancelling source in devs.
func echoCancelSource(devs []platform.Device) (platform.Device, bool) {
	for _, d := range devs {
		if isEchoCancel(d) {
			return d, true
		}
	}
	return platform.Device{}, false
}

// measure listens to dev for d, drawing a live meter on w, and keeps nothing
// but the numbers. Raw level is measured: the level normaliser is not applied.
func measure(ctx context.Context, a platform.Audio, dev platform.Device, d time.Duration, w io.Writer) (micStats, error) {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	id := dev.ID
	if dev.Default {
		id = ""
	}
	frames, err := a.Capture(cctx, id, audio.Speech)
	if err != nil {
		return micStats{}, fmt.Errorf("open %q: %w", dev.Name, err)
	}
	var (
		st      = micStats{RMSDB: math.Inf(-1), PeakDB: math.Inf(-1)}
		sumSq   float64
		n       int
		blkSum  float64
		blkN    int
		blkPeak float64
		next    = 250 * time.Millisecond
	)
	for fr := range frames {
		st.Frames++
		for _, s := range fr.Samples {
			v := float64(s)
			sumSq += v * v
			blkSum += v * v
			blkPeak = math.Max(blkPeak, math.Abs(v))
		}
		n += len(fr.Samples)
		blkN += len(fr.Samples)
		if t := fr.Offset + fr.Duration(); t >= next && blkN > 0 {
			next = t + 250*time.Millisecond
			_, _ = fmt.Fprintln(w, meterLine(toDB(math.Sqrt(blkSum/float64(blkN))), toDB(blkPeak), t))
			blkSum, blkN, blkPeak = 0, 0, 0
		}
		st.PeakDB = math.Max(st.PeakDB, toDB(math.Abs(float64(peakOf(fr.Samples)))))
	}
	if err := ctx.Err(); err != nil {
		return st, err
	}
	if n > 0 {
		st.RMSDB = toDB(math.Sqrt(sumSq / float64(n)))
	}
	return st, nil
}

func peakOf(s []float32) float32 {
	var p float32
	for _, v := range s {
		if v < 0 {
			v = -v
		}
		if v > p {
			p = v
		}
	}
	return p
}

func toDB(x float64) float64 {
	if x <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(x)
}

// meterLine draws one level reading: a bar from -60 to 0 dBFS.
func meterLine(rms, peak float64, at time.Duration) string {
	const width = 40
	pos := func(db float64) int {
		if math.IsInf(db, -1) || db < -60 {
			return 0
		}
		return min(width, int((db+60)/60*width+0.5))
	}
	bar := []byte(strings.Repeat(" ", width))
	for i := 0; i < pos(rms); i++ {
		bar[i] = '#'
	}
	if p := pos(peak); p > 0 {
		bar[p-1] = '|'
	}
	return fmt.Sprintf("  %4.1fs [%s] rms %s peak %s", at.Seconds(), bar, fmtDB(rms), fmtDB(peak))
}

func fmtDB(db float64) string {
	if math.IsInf(db, -1) {
		return "  -inf dBFS"
	}
	return fmt.Sprintf("%6.1f dBFS", db)
}

// report prints the result and what to do about a poor level.
func report(w io.Writer, st micStats, devs []platform.Device, tested platform.Device) {
	if st.Frames == 0 {
		_, _ = fmt.Fprintln(w, "\nNo audio arrived from this source.")
		return
	}
	_, _ = fmt.Fprintf(w, "\nOverall: rms %s, peak %s\n", strings.TrimSpace(fmtDB(st.RMSDB)), strings.TrimSpace(fmtDB(st.PeakDB)))
	switch {
	case st.PeakDB < micSilentPeak:
		_, _ = fmt.Fprintln(w, "WARNING: silence. The microphone may be muted, unplugged or the wrong source.")
	case st.RMSDB < micLowRMS || st.PeakDB < micLowPeak:
		_, _ = fmt.Fprintln(w, "WARNING: the level is too low for reliable wake-word detection.")
	case st.PeakDB > micClipPeak:
		_, _ = fmt.Fprintln(w, "WARNING: the signal reaches full scale and may be clipping. Lower the input volume.")
	default:
		_, _ = fmt.Fprintln(w, "OK: the level is good for wake-word detection.")
		return
	}
	if st.PeakDB > micClipPeak && st.PeakDB >= micSilentPeak {
		return
	}
	if ec, ok := echoCancelSource(devs); ok && ec.ID != tested.ID {
		_, _ = fmt.Fprintf(w, "  - Try the echo-cancelling source %q (it often has automatic gain too), or set audio.prefer_echo_cancel = true.\n", ec.Name)
	}
	_, _ = fmt.Fprintln(w, "  - Raise the input volume in your sound settings (for example `pactl set-source-volume @DEFAULT_SOURCE@ 100%`), and move closer to the microphone.")
	_, _ = fmt.Fprintln(w, "  - Footman also normalises quiet input itself unless audio.agc is false, up to audio.agc_max_gain_db.")
}

// offerMicCheck is the end of `footman setup`: it offers the microphone check
// and runs it if the user agrees.
func offerMicCheck(ctx context.Context, p platform.Platform, configPath string, in *bufio.Reader, out io.Writer) error {
	o := micOptions{Audio: p.Audio(), ConfigPath: configPath, Duration: 5 * time.Second, Ask: true, In: in, Out: out}
	yes, err := askYes(o, "\nCheck your microphone level now?")
	if err != nil || !yes {
		return err
	}
	return micCheck(ctx, o)
}
