//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
	platformfake "github.com/irobinson010/footman/internal/platform/fake"
)

// syncBuffer is a bytes.Buffer safe for concurrent use that can wait for
// text to appear.
type syncBuffer struct {
	mu      sync.Mutex
	b       bytes.Buffer
	changed chan struct{}
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{changed: make(chan struct{}, 1)} }

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor waits until the buffer contains text.
func (s *syncBuffer) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(s.String(), text) {
		select {
		case <-s.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for %q in:\n%s", text, s.String())
		}
	}
}

type testDaemon struct {
	socket, config, data string
	platform             *platformfake.Platform
	log                  *syncBuffer
	cancel               context.CancelFunc
	done                 chan error
}

// shortTempDir returns a temporary directory whose path is short enough for
// a Unix socket.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startTimeout bounds how long startDaemon waits for the socket. The native
// tests raise it for model loading and GPU warm-up.
var startTimeout = 5 * time.Second

// startDaemon runs the daemon with --dev-fakes on a temporary socket and
// returns once it is accepting connections.
func startDaemon(t *testing.T) *testDaemon {
	t.Helper()
	if len(tweak) == 0 {
		return startDaemonWith(t, nil)
	}
	return startDaemonWith(t, func(o *daemonOptions) {
		for _, f := range tweak {
			f(o)
		}
	})
}

// startDaemonWith is startDaemon with the options edited by edit (if not
// nil) before the daemon starts; the config file may be written by then.
func startDaemonWith(t *testing.T, edit func(*daemonOptions)) *testDaemon {
	t.Helper()
	dir := shortTempDir(t)
	d := &testDaemon{
		socket: filepath.Join(dir, "run", "footman.sock"),
		config: filepath.Join(dir, "config", "config.toml"),
		data:   filepath.Join(dir, "data"),
		log:    newSyncBuffer(),
		done:   make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	ready := make(chan struct{})
	opts := daemonOptions{
		socket: d.socket, configPath: d.config, dataDir: d.data, devFakes: true, fakesOnly: true,
		deps:  func(deps *core.Deps) { d.platform = deps.Platform.(*platformfake.Platform) },
		ready: func() { close(ready) },
	}
	if edit != nil {
		edit(&opts)
	}
	go func() { d.done <- runDaemon(ctx, opts, d.log) }()
	select {
	case <-ready:
	case err := <-d.done:
		t.Fatalf("daemon exited early: %v\n%s", err, d.log.String())
	case <-time.After(startTimeout):
		t.Fatalf("daemon not ready after %s:\n%s", startTimeout, d.log.String())
	}
	t.Cleanup(func() { d.stop(t) })
	return d
}

// stop shuts the daemon down as SIGTERM would and checks it exits cleanly.
func (d *testDaemon) stop(t *testing.T) {
	t.Helper()
	if d.cancel == nil {
		return
	}
	d.cancel()
	d.cancel = nil
	select {
	case err := <-d.done:
		if err != nil {
			t.Errorf("daemon exited with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down within 5s")
	}
}

// cli runs a footman command against the daemon.
func (d *testDaemon) cli(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	args = append([]string{args[0], "--socket", d.socket}, args[1:]...)
	code = run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestDaemonIntegration is the issue's acceptance test: the daemon starts on
// a temporary socket with fakes, status.get and say.text round-trip,
// events.subscribe streams pipeline states, and `footman stop` triggers the
// break-glass stop.
func TestDaemonIntegration(t *testing.T) {
	d := startDaemon(t)

	st, err := os.Stat(d.socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v, mode %v; want 0600", err, st.Mode())
	}

	code, out, errOut := d.cli(t, "status")
	if code != 0 || !strings.Contains(out, "state:       idle") || !strings.Contains(out, "platform=fake") || !strings.Contains(out, "--dev-fakes") {
		t.Fatalf("status = %d\n%s%s", code, out, errOut)
	}
	code, out, _ = d.cli(t, "status", "--json")
	var status api.StatusResult
	if err := json.Unmarshal([]byte(out), &status); code != 0 || err != nil || status.API != "v1" || status.Socket != d.socket {
		t.Fatalf("status --json = %d %v: %s", code, err, out)
	}

	// Watch the pipeline over the API while the CLI drives it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := api.Dial(ctx, d.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Call(ctx, "events.subscribe", api.SubscribeParams{Topics: []string{"pipeline.state", "pipeline.stop"}}, nil); err != nil {
		t.Fatal(err)
	}
	next := func() (string, string) {
		t.Helper()
		select {
		case n := <-c.Notifications():
			var ev api.Event
			if err := json.Unmarshal(n.Params, &ev); err != nil {
				t.Fatal(err)
			}
			return ev.Topic, string(ev.Data)
		case <-ctx.Done():
			t.Fatal("no event in time")
		}
		return "", ""
	}

	if code, out, errOut := d.cli(t, "say", "open", "youtube"); code != 0 {
		t.Fatalf("say = %d %s%s", code, out, errOut)
	}
	if got := d.platform.Launcher().(*platformfake.Launcher).URLs(); !slices.Equal(got, []string{"https://www.youtube.com"}) {
		t.Errorf("opened %v", got)
	}
	var states []string
	for range 4 {
		_, data := next()
		states = append(states, data)
	}
	want := []string{
		`{"from":"idle","to":"routing","turn":1}`,
		`{"from":"routing","to":"acknowledging","turn":1}`,
		`{"from":"acknowledging","to":"fast_path","turn":1}`,
		`{"from":"fast_path","to":"idle","turn":0}`,
	}
	if !slices.Equal(states, want) {
		t.Errorf("states:\n%s\nwant:\n%s", strings.Join(states, "\n"), strings.Join(want, "\n"))
	}

	if code, _, _ := d.cli(t, "say", "--dictate", "hello", "world"); code != 0 {
		t.Fatalf("say --dictate = %d", code)
	}
	for range 4 { // routing, cleaning, inserting, idle
		next()
	}
	if got := d.platform.Input().(*platformfake.Input).Typed(); !slices.Equal(got, []string{"hello world"}) {
		t.Errorf("typed %v", got)
	}

	// The break-glass stop.
	code, out, errOut = d.cli(t, "stop")
	if code != 0 || !strings.Contains(out, "Stopped everything") {
		t.Fatalf("stop = %d %s%s", code, out, errOut)
	}
	if topic, data := next(); topic != "pipeline.stop" || data != `{"source":"call","jobs":[],"turns":0}` {
		t.Errorf("after stop got %s %s", topic, data)
	}
	// The fake microphone's stream ends at once (nothing is queued), so the
	// loop has stopped and status says so; the stop itself sets the muted flag.
	if _, out, _ := d.cli(t, "status", "--json"); !strings.Contains(out, `"muted":true`) {
		t.Errorf("status after stop:\n%s", out)
	}

	// Graceful shutdown removes the socket.
	d.stop(t)
	if _, err := os.Stat(d.socket); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
	if !strings.Contains(d.log.String(), "shutting down") {
		t.Errorf("log:\n%s", d.log.String())
	}
	if code, _, errOut := d.cli(t, "status"); code != 1 || !strings.Contains(errOut, "not running") {
		t.Errorf("status with no daemon = %d %s", code, errOut)
	}
}

func TestEventsCommand(t *testing.T) {
	d := startDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	stdout, stderr := newSyncBuffer(), newSyncBuffer()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"events", "--socket", d.socket, "--topic", "pipeline.fast_path,pipeline.route"}, stdout, stderr)
	}()
	stderr.waitFor(t, "streaming events")

	if code, _, errOut := d.cli(t, "say", "open github"); code != 0 {
		t.Fatalf("say: %s", errOut)
	}
	stdout.waitFor(t, `pipeline.fast_path   {"turn":1,"rule":"open"}`)
	if !strings.Contains(stdout.String(), `pipeline.route       {"turn":1,"route":"command","text":"open github"}`) {
		t.Errorf("events output:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "pipeline.state") {
		t.Errorf("filtered topic shown:\n%s", stdout.String())
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("events exited %d", code)
	}
}

func TestSayErrorsReachTheCLI(t *testing.T) {
	d := startDaemon(t)
	// The fake AI has no reply queued, so a command it must answer fails.
	code, _, errOut := d.cli(t, "say", "what", "time", "is", "it")
	if code != 1 || !strings.Contains(errOut, "no reply queued") {
		t.Errorf("say = %d %q", code, errOut)
	}
}

func TestDaemonRefusesSecondInstance(t *testing.T) {
	d := startDaemon(t)
	err := runDaemon(context.Background(), daemonOptions{socket: d.socket, configPath: d.config, dataDir: d.data, devFakes: true, fakesOnly: true}, newSyncBuffer())
	if !errors.Is(err, api.ErrRunning) {
		t.Errorf("second daemon = %v, want ErrRunning", err)
	}
}

func TestDaemonNeedsRealImplementationsWithoutDevFakes(t *testing.T) {
	dir := shortTempDir(t)
	var log bytes.Buffer
	code := run(context.Background(), []string{"daemon", "--socket", filepath.Join(dir, "s.sock"),
		"--config", filepath.Join(dir, "config.toml"), "--data", dir}, &log, &log)
	if code != 1 || !strings.Contains(log.String(), "--dev-fakes") {
		t.Errorf("daemon without fakes = %d:\n%s", code, log.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "s.sock")); !os.IsNotExist(err) {
		t.Error("socket created although the daemon could not start")
	}
}

// noHotkeys is a platform whose input cannot register hotkeys, as on a
// session without input access. The microphone loop stops with that error.
type noHotkeys struct{ platform.Platform }

func (p noHotkeys) Input() platform.Input { return noHotkeysInput{p.Platform.Input()} }

type noHotkeysInput struct{ platform.Input }

func (noHotkeysInput) Hotkeys(context.Context, []string) (<-chan platform.HotkeyEvent, error) {
	return nil, errors.New("linux input: platform: not supported here")
}

// TestDaemonReportsStoppedMicrophone checks that status says the microphone
// is stopped, and why, rather than on, once its loop has failed.
func TestDaemonReportsStoppedMicrophone(t *testing.T) {
	d := startDaemonWith(t, func(o *daemonOptions) {
		prev := o.deps
		o.deps = func(deps *core.Deps) {
			prev(deps)
			deps.Platform = noHotkeys{deps.Platform}
		}
	})
	d.log.waitFor(t, "pipeline stopped: linux input: platform: not supported here")

	const want = "  microphone:  stopped: linux input: platform: not supported here\n"
	if _, out, _ := d.cli(t, "status"); !strings.Contains(out, want) {
		t.Errorf("status:\n%s, want the line %q", out, want)
	}
	_, out, _ := d.cli(t, "status", "--json")
	if !strings.Contains(out, `"microphone":{"state":"stopped","error":"linux input: platform: not supported here"}`) {
		t.Errorf("status --json = %s", out)
	}
}

// TestDaemonRejectsLongSocketPath checks that a socket path the OS cannot
// bind is refused up front, before the daemon opens the microphone or creates
// its data directory.
func TestDaemonRejectsLongSocketPath(t *testing.T) {
	dir := shortTempDir(t)
	socket := filepath.Join(dir, strings.Repeat("s", 200)+".sock")
	var log bytes.Buffer
	code := run(context.Background(), []string{"daemon", "--dev-fakes", "--socket", socket,
		"--config", filepath.Join(dir, "config.toml"), "--data", filepath.Join(dir, "data")}, &log, &log)
	if code != 1 || !strings.Contains(log.String(), "socket path too long") || !strings.Contains(log.String(), "XDG_RUNTIME_DIR") {
		t.Errorf("daemon with a long socket path = %d:\n%s", code, log.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Error("data directory created although the socket path was refused")
	}
}

func TestDaemonBadConfig(t *testing.T) {
	dir := shortTempDir(t)
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[dictation]\ninsert = \"shout\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	code := run(context.Background(), []string{"daemon", "--dev-fakes", "--socket", filepath.Join(dir, "s.sock"),
		"--config", cfg, "--data", dir}, &log, &log)
	if code != 1 || !strings.Contains(log.String(), "dictation.insert") {
		t.Errorf("daemon with bad config = %d:\n%s", code, log.String())
	}
}
