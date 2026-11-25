//go:build linux && integration && native

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform/linux"
	"github.com/irobinson010/footman/internal/speech/models"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

// TestE2EPushToTalk is the Sprint 1 exit test (S1-08, ADR-0030), run against
// the real footman binary:
//
//  1. `footman setup` in a temporary HOME inside a private, headless KWin
//     session (linux.StartNested): it binds F8 through the GlobalShortcuts
//     portal, gets the RemoteDesktop permission, finds the models already
//     there and warms up. The service step is skipped (--no-service): it is
//     covered with a fake systemctl in internal/platform/linux.
//  2. `footman daemon` with the real Linux platform, Silero VAD and speech
//     to text, no flags.
//  3. A Qt text field has focus. F8 is pressed through the session's
//     RemoteDesktop portal (the path a physical key takes), a LibriSpeech
//     clip plays into a virtual audio sink (footman_test_*) whose monitor is
//     the configured microphone, and F8 is released. The transcript must
//     appear in the field.
//  4. The same with a 10-second clip, several times, timing release to text.
//  5. Idle memory once the models have unloaded, then `footman uninstall
//     --purge`, which must leave nothing behind.
//
// It never touches the real desktop, microphone or speakers: the session has
// its own Wayland socket, D-Bus bus and XDG folders, and the only audio
// device used is the virtual sink, unloaded at the end.
//
//	eval "$(scripts/native/whispercpp.sh env)"
//	FOOTMAN_TEST_MODELS=~/.cache/footman-test-models \
//	  go test -tags 'integration native' -v -count=1 -timeout 15m ./cmd/footman/ -run TestE2EPushToTalk
//
// FOOTMAN_E2E_STT, FOOTMAN_E2E_MODEL and FOOTMAN_E2E_ACCEL pick the engine
// (default whispercpp, whisper-tiny.en, cpu); FOOTMAN_E2E_RUNS repeats the
// 10-second utterance (default 3); FOOTMAN_E2E_PULSE names the audio server
// (default unix:/run/user/<uid>/pulse/native).
func TestE2EPushToTalk(t *testing.T) {
	for _, tool := range append([]string{"pactl", "paplay", "python3"}, linux.NestedTools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if exec.Command("python3", "-c", "import PyQt6").Run() != nil {
		t.Skip("python3 module PyQt6 is not installed")
	}
	engine := envOr("FOOTMAN_E2E_STT", "whispercpp")
	model := envOr("FOOTMAN_E2E_MODEL", "whisper-tiny.en")
	acc := envOr("FOOTMAN_E2E_ACCEL", "cpu")
	runs, _ := strconv.Atoi(envOr("FOOTMAN_E2E_RUNS", "3"))
	testModels := speechtest.ModelsDir(t, "silero-vad", model)
	pulse := envOr("FOOTMAN_E2E_PULSE", fmt.Sprintf("unix:/run/user/%d/pulse/native", os.Getuid()))
	e := &e2e{t: t, pulse: pulse}
	if err := e.pactl("info"); err != nil {
		t.Skipf("no PulseAudio/PipeWire server at %s: %v", pulse, err)
	}

	// The binary under test.
	bin := filepath.Join(t.TempDir(), "footman")
	build := exec.Command("go", "build", "-tags", "native", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	n, err := linux.StartNested()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Stop)
	e.n = n
	home := filepath.Join(n.Root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	e.env = n.Environ("HOME="+home, "PULSE_SERVER="+pulse, "FOOTMAN_SOCKET=")
	// FOOTMAN_E2E_ENV adds variables for footman (space-separated
	// KEY=VALUE), for experiments such as MALLOC_ARENA_MAX=2.
	e.env = append(e.env, strings.Fields(os.Getenv("FOOTMAN_E2E_ENV"))...)
	e.bin = bin
	cfgDir := filepath.Join(n.Env["XDG_CONFIG_HOME"], "footman")
	dataDir := filepath.Join(n.Env["XDG_DATA_HOME"], "footman")
	cacheDir := filepath.Join(n.Env["XDG_CACHE_HOME"], "footman")

	// The virtual microphone: a null sink whose monitor Footman records.
	sink := e.virtualSink()

	// The config the user would have, pointing at the virtual microphone,
	// and the models already downloaded (as links, so uninstall removing
	// them leaves the originals alone).
	unload := envOr("FOOTMAN_E2E_UNLOAD", "20s")
	cfg := fmt.Sprintf("version = 1\n[input]\nwake_word = false\nmicrophone = %q\n[speech]\nunload_after = %q\n"+
		"[speech.stt]\nengine = %q\nmodel = %q\naccelerator = %q\n", sink+".monitor", unload, engine, model, acc)
	mustWrite(t, filepath.Join(cfgDir, "config.toml"), cfg)
	for _, id := range []string{"silero-vad", model} {
		m, _ := models.Lookup(id)
		if err := os.MkdirAll(filepath.Join(dataDir, "models"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(m.Path(testModels), m.Path(filepath.Join(dataDir, "models"))); err != nil {
			t.Fatal(err)
		}
	}

	// The test's own portal session presses keys and answers the shortcut
	// dialog; the RemoteDesktop dialog is approved in advance.
	if err := n.AllowRemoteDesktop(); err != nil {
		t.Fatal(err)
	}
	keys := n.NewPlatform(filepath.Join(t.TempDir(), "keys.token"))
	t.Cleanup(func() { _ = keys.Close() })
	actx, stopApprover := context.WithCancel(context.Background())
	approver := keys.ApproveShortcutDialogs(actx)
	t.Cleanup(func() { stopApprover(); <-approver })

	// 1. Setup.
	start := time.Now()
	out, err := e.footman(10*time.Minute, "setup", "--non-interactive", "--hotkey", "F8", "--no-service")
	t.Logf("footman setup (%s):\n%s", time.Since(start).Round(time.Millisecond), out)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, want := range []string{"F8 bound with the desktop (dictation)", "allowed to type and paste", "silero-vad already downloaded", model + " already downloaded"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output lacks %q", want)
		}
	}
	stopApprover()
	<-approver

	// 2. The daemon, as the service would run it.
	d := e.startDaemon()
	st := e.status()
	t.Logf("daemon ready in %s; engines %v", d.ready.Round(time.Millisecond), st.Implementations)
	if b, err := os.ReadFile(d.log); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, "speech-to-text") || strings.Contains(l, "VAD") {
				t.Log(l)
			}
		}
	}
	if st.Implementations["platform"] != "linux" || st.Implementations["stt"] != engine || st.Implementations["vad"] != "silero" || st.DevFakes {
		t.Fatalf("daemon engines %v (dev fakes %v)", st.Implementations, st.DevFakes)
	}
	if st.Microphone.State != core.MicPushToTalk {
		t.Errorf("microphone %+v, want push_to_talk", st.Microphone)
	}
	t.Logf("RSS with models loaded, idle: %d MB", rssMB(t, d.cmd.Process.Pid))

	// 3. The text field, and a 5-second utterance.
	sinkLog := filepath.Join(t.TempDir(), "qt.jsonl")
	qt := exec.Command("python3", linux.NestedTestdata("qtsink.py"), sinkLog)
	qt.Env = n.Environ("HOME=" + home)
	qt.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := qt.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-qt.Process.Pid, syscall.SIGTERM); _ = qt.Wait() })
	e.waitSink(sinkLog, 20*time.Second, func(r sinkRecord) bool { return r.Ready })
	e.waitActive(keys, "qtsink")

	clip := speechtest.ClipPath(t)
	one := e.dictate(keys, sink, sinkLog, "", clip)
	if w := speechtest.WER(speechtest.Reference, one.text); w > 0.25 {
		t.Errorf("5 s utterance: word error rate %.0f%%: %q", 100*w, one.text)
	}
	t.Logf("5 s utterance: %q, release to text %s", one.text, one.latency.Round(time.Millisecond))

	// 4. 10-second utterances, timed.
	ref, clip10 := speechtest.Reference10, speechtest.Clip10Path(t)
	var lat []time.Duration
	typed := one.full
	for i := range max(runs, 1) {
		r := e.dictate(keys, sink, sinkLog, typed, clip10)
		typed = r.full
		if w := speechtest.WER(ref, r.text); w > 0.25 {
			t.Errorf("10 s utterance %d: word error rate %.0f%%: %q", i+1, 100*w, r.text)
		}
		t.Logf("10 s utterance %d: release to text %s (daemon: %.0f ms): %q", i+1, r.latency.Round(time.Millisecond), r.daemonMS, r.text)
		lat = append(lat, r.latency)
	}
	t.Logf("10 s release-to-text over %d runs: %s", len(lat), summary(lat))
	// The same clip from its file through the same daemon, for comparison.
	for range 2 {
		if code, out, errOut := e.cli("transcribe", "--json", "--no-vad", clip10); code == 0 {
			var r api.TranscribeResult
			if json.Unmarshal([]byte(out), &r) == nil {
				t.Logf("the 10 s file through `footman transcribe --no-vad`: speech-to-text %.0f ms", r.STTMS)
			}
		} else {
			t.Logf("footman transcribe: %s", errOut)
		}
	}
	if b, err := os.ReadFile(d.log); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, ": turn ") {
				t.Log(l)
			}
		}
	}
	if _, out, _ := e.cli("status"); out != "" {
		t.Logf("footman status:\n%s", out)
	}

	// 5. Idle memory once the models have unloaded.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		loaded := false
		for _, m := range e.status().Models.Models {
			loaded = loaded || m.Loaded
		}
		if !loaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("models still loaded after %s: %+v", unload, e.status().Models)
		}
		time.Sleep(time.Second)
	}
	time.Sleep(2 * time.Second)
	t.Logf("RSS with models unloaded: %d MB (%s); largest parts:\n%s", rssMB(t, d.cmd.Process.Pid), rollup(d.cmd.Process.Pid), rssParts(d.cmd.Process.Pid, 12))

	// Stop the daemon as systemd would, then uninstall.
	d.stop()
	stopApprover()
	out, err = e.footman(time.Minute, "uninstall", "--purge", "--non-interactive")
	t.Logf("footman uninstall:\n%s", out)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, p := range []string{cfgDir, dataDir, cacheDir, bin,
		filepath.Join(n.Env["XDG_DATA_HOME"], "applications", linux.AppID+".desktop"),
		filepath.Join(n.Env["XDG_RUNTIME_DIR"], "footman.sock")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("left behind: %s (%v)", p, err)
		}
	}
	// Nothing else with Footman's name anywhere in the temporary HOME or
	// XDG folders. (KDE's own shortcut list still names the F8 shortcut;
	// ADR-0029 leaves that to System Settings.)
	for _, root := range []string{home, n.Env["XDG_CONFIG_HOME"], n.Env["XDG_DATA_HOME"], n.Env["XDG_CACHE_HOME"], n.Env["XDG_STATE_HOME"]} {
		_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err == nil && strings.Contains(strings.ToLower(de.Name()), "footman") {
				t.Errorf("left behind: %s", p)
			}
			return nil
		})
	}
	for _, id := range []string{"silero-vad", model} {
		if m, _ := models.Lookup(id); !m.Installed(testModels) {
			t.Fatalf("uninstall removed the test model %s from %s", id, testModels)
		}
	}
}

type e2e struct {
	t     *testing.T
	n     *linux.Nested
	env   []string
	bin   string
	pulse string
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pactl runs pactl against the configured audio server only.
func (e *e2e) pactl(args ...string) error {
	_, err := e.pactlOut(args...)
	return err
}

func (e *e2e) pactlOut(args ...string) (string, error) {
	cmd := exec.Command("pactl", args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PULSE_SERVER=" + e.pulse}
	out, err := cmd.Output()
	return string(out), err
}

// virtualSink creates a footman_test_* null sink, unloaded when the test
// ends, and returns its name.
func (e *e2e) virtualSink() string {
	e.t.Helper()
	name := fmt.Sprintf("footman_test_e2e_%d", os.Getpid())
	id, err := e.pactlOut("load-module", "module-null-sink", "sink_name="+name, "sink_properties=device.description="+name)
	if err != nil {
		e.t.Skipf("cannot create a virtual sink: %v", err)
	}
	e.t.Cleanup(func() { _ = e.pactl("unload-module", strings.TrimSpace(id)) })
	return name
}

// footman runs the binary under test inside the session and returns its
// combined output.
func (e *e2e) footman(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cli runs a client command; code is its exit status.
func (e *e2e) cli(args ...string) (code int, stdout, stderr string) {
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env
	var o, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &eb
	err := cmd.Run()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return code, o.String(), eb.String()
}

func (e *e2e) status() api.StatusResult {
	e.t.Helper()
	code, out, errOut := e.cli("status", "--json")
	if code != 0 {
		e.t.Fatalf("footman status = %d: %s", code, errOut)
	}
	var s api.StatusResult
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		e.t.Fatalf("status: %v: %s", err, out)
	}
	return s
}

type e2eDaemon struct {
	cmd   *exec.Cmd
	log   string
	ready time.Duration
	t     *testing.T
	done  chan struct{}
}

func (e *e2e) startDaemon() *e2eDaemon {
	e.t.Helper()
	log := filepath.Join(e.t.TempDir(), "daemon.log")
	f, err := os.Create(log)
	if err != nil {
		e.t.Fatal(err)
	}
	cmd := exec.Command(e.bin, "daemon")
	cmd.Env = e.env
	cmd.Stdout, cmd.Stderr = f, f
	start := time.Now()
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	d := &e2eDaemon{cmd: cmd, log: log, t: e.t, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); _ = f.Close(); close(d.done) }()
	e.t.Cleanup(func() {
		d.stop()
		if e.t.Failed() {
			b, _ := os.ReadFile(log)
			e.t.Logf("daemon log:\n%s", b)
		}
	})
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if code, _, _ := e.cli("status"); code == 0 {
			break
		}
		select {
		case <-d.done:
			b, _ := os.ReadFile(log)
			e.t.Fatalf("daemon exited:\n%s", b)
		default:
		}
		if time.Now().After(deadline) {
			e.t.Fatal("daemon not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.ready = time.Since(start)
	return d
}

// stop sends SIGTERM, as systemd does, and waits for a clean exit.
func (d *e2eDaemon) stop() {
	select {
	case <-d.done:
		return
	default:
	}
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.done:
		if code := d.cmd.ProcessState.ExitCode(); code != 0 {
			d.t.Errorf("daemon exited with %d", code)
		}
	case <-time.After(15 * time.Second):
		_ = d.cmd.Process.Kill()
		d.t.Error("daemon did not stop within 15 s of SIGTERM")
	}
}

type sinkRecord struct {
	Ready bool    `json:"ready"`
	Text  string  `json:"text"`
	T     float64 `json:"t"`
}

func readSink(path string) []sinkRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []sinkRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r sinkRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (e *e2e) waitSink(path string, d time.Duration, ok func(sinkRecord) bool) sinkRecord {
	e.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, r := range readSink(path) {
			if ok(r) {
				return r
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for the text field (%s)", path)
	return sinkRecord{}
}

func (e *e2e) waitActive(p *linux.Platform, app string) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		w, err := p.Windows().Active(ctx)
		cancel()
		if err == nil && strings.Contains(strings.ToLower(w.App), app) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("%s never had focus", app)
}

type dictated struct {
	text, full string
	latency    time.Duration
	daemonMS   float64
}

// dictate holds F8, plays the clips into the virtual microphone, releases F8
// and waits for the field to settle on new text after before. latency is
// from the release to the last change of the field.
func (e *e2e) dictate(keys *linux.Platform, sink, sinkLog, before string, clips ...string) dictated {
	e.t.Helper()
	ctx := context.Background()
	if err := keys.Key(ctx, linux.KeysymF8, true); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.status().Microphone.State != core.MicRecording {
		if time.Now().After(deadline) {
			e.t.Fatalf("F8 did not start recording: %+v", e.status().Microphone)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, c := range clips {
		play := exec.Command("paplay", "--device="+sink, c)
		play.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PULSE_SERVER=" + e.pulse}
		if out, err := play.CombinedOutput(); err != nil {
			e.t.Fatalf("paplay: %v: %s", err, out)
		}
	}
	released := time.Now()
	if err := keys.Key(ctx, linux.KeysymF8, false); err != nil {
		e.t.Fatal(err)
	}
	// The text is typed (or pasted) at once; wait for it to stop changing.
	var last sinkRecord
	deadline = time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		recs := readSink(sinkLog)
		if len(recs) > 0 {
			r := recs[len(recs)-1]
			if r.Text != before && r.Text == last.Text && time.Since(time.Unix(0, int64(r.T*1e9))) > 700*time.Millisecond {
				break
			}
			last = r
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last.Text == before {
		e.t.Fatalf("nothing was inserted after releasing F8")
	}
	at := time.Unix(0, int64(last.T*1e9))
	d := dictated{full: last.Text, text: strings.TrimPrefix(last.Text, before), latency: at.Sub(released)}
	if ld := e.status().LastDictation; ld != nil {
		d.daemonMS = ld.LatencyMS
	}
	return d
}

func summary(ds []time.Duration) string {
	if len(ds) == 0 {
		return "none"
	}
	lo, hi, sum := ds[0], ds[0], time.Duration(0)
	for _, d := range ds {
		lo, hi, sum = min(lo, d), max(hi, d), sum+d
	}
	mean := sum / time.Duration(len(ds))
	return fmt.Sprintf("mean %s, min %s, max %s", mean.Round(time.Millisecond), lo.Round(time.Millisecond), hi.Round(time.Millisecond))
}

// rssParts lists the n mappings of a process with the most resident memory,
// summed by file (anonymous memory as "[anon]"), from /proc/<pid>/smaps.
func rssParts(pid, n int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps", pid))
	if err != nil {
		return err.Error()
	}
	byName := map[string]int{}
	name := ""
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 && strings.Contains(f[0], "-") && !strings.HasSuffix(f[0], ":") {
			name = "[anon]"
			if len(f) >= 6 {
				name = f[5]
			}
			continue
		}
		if len(f) >= 2 && f[0] == "Rss:" {
			kb, _ := strconv.Atoi(f[1])
			byName[name] += kb
		}
	}
	type part struct {
		name string
		kb   int
	}
	var parts []part
	for k, v := range byName {
		parts = append(parts, part{k, v})
	}
	slices.SortFunc(parts, func(a, b part) int { return b.kb - a.kb })
	var out []string
	for i, p := range parts {
		if i == n || p.kb < 512 {
			break
		}
		out = append(out, fmt.Sprintf("  %6.1f MB  %s", float64(p.kb)/1024, p.name))
	}
	return strings.Join(out, "\n")
}

// rollup summarises /proc/<pid>/smaps_rollup: the memory only this process
// uses (private) against what it shares with others (program and library
// code the kernel can drop and read back).
func rollup(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return err.Error()
	}
	v := map[string]int{}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 {
			v[strings.TrimSuffix(f[0], ":")], _ = strconv.Atoi(f[1])
		}
	}
	mb := func(kb int) float64 { return float64(kb) / 1024 }
	return fmt.Sprintf("PSS %.0f MB, private %.0f MB (dirty %.0f MB), shared %.0f MB, anonymous %.0f MB",
		mb(v["Pss"]), mb(v["Private_Clean"]+v["Private_Dirty"]), mb(v["Private_Dirty"]),
		mb(v["Shared_Clean"]+v["Shared_Dirty"]), mb(v["Anonymous"]))
}

// rssMB is a process's resident memory in MB.
func rssMB(t *testing.T, pid int) int {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")))
			return kb / 1024
		}
	}
	return 0
}
