package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// whispercli drives a prebuilt whisper.cpp `whisper-cli` binary, one process
// per clip. It is pure Go (no cgo), so it cross-compiles anywhere, and it is
// the route for Windows, where the official release zips carry whisper-cli.exe.
// The model is reloaded for every clip, so latency is the wall time minus the
// load time the binary reports about itself. That makes it slightly less exact
// than the in-process engines, and it can't report peak RSS of the child on
// Windows; it also doubles as a cross-check of the Go-binding overhead
// (compare its inner time with the cgo engine on the same machine).

func init() {
	engines["whispercli"] = engineEntry{
		New:    func() Engine { return &cliEngine{} },
		Accels: []string{"cpu", "cuda", "vulkan"}, // whichever the supplied binary was built for
	}
}

type cliEngine struct {
	bin, model, accel string
	threads           int
}

func (e *cliEngine) ModelBytes() int64 { return fileBytes(e.model) }

func (e *cliEngine) Info() string {
	return fmt.Sprintf("external %s, accel label %s (set by the binary you supplied)", e.bin, e.accel)
}

func (e *cliEngine) Load(o LoadOpts) error {
	if o.Model.GGML == "" {
		return fmt.Errorf("model %s has no ggml build", o.Model.ID)
	}
	if o.CLIPath == "" {
		return fmt.Errorf("whispercli needs -whisper-cli PATH")
	}
	e.bin = o.CLIPath
	e.model = filepath.Join(o.ModelsDir, "whispercpp", o.Model.GGML)
	if _, err := os.Stat(e.model); err != nil {
		return err
	}
	if _, err := os.Stat(e.bin); err != nil {
		return err
	}
	e.accel, e.threads = o.Accel, o.Threads
	return nil
}

var (
	reLoad  = regexp.MustCompile(`load time\s*=\s*([\d.]+)\s*ms`)
	reTotal = regexp.MustCompile(`total time\s*=\s*([\d.]+)\s*ms`)
)

func parseMS(re *regexp.Regexp, s string) time.Duration {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return time.Duration(f * float64(time.Millisecond))
}

func (e *cliEngine) Transcribe(c *Clip) (Result, error) {
	args := []string{"-m", e.model, "-f", c.Path, "-t", strconv.Itoa(e.threads), "-l", "en", "-nt"}
	if e.accel == "cpu" {
		args = append(args, "-ng") // no GPU, even if the binary has a GPU backend
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(e.bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	t := time.Now()
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("%v: %s", err, tail(stderr.String()))
	}
	wall := time.Since(t)
	load := parseMS(reLoad, stderr.String())
	total := parseMS(reTotal, stderr.String())
	r := Result{Text: strings.Join(strings.Fields(stdout.String()), " "), Latency: wall - load}
	if r.Latency <= 0 {
		r.Latency = wall
	}
	if total > load {
		r.Inner = total - load
	}
	return r, nil
}

func (e *cliEngine) Close() {}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return s
}
