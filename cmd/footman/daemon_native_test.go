//go:build native && unix

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/api"
	"github.com/irobinson010/footman/internal/install"
	"github.com/irobinson010/footman/internal/speech/accel"
	"github.com/irobinson010/footman/internal/speech/speechtest"
)

// TestDaemonTranscribesWAV is S1-06's acceptance test: the daemon, with the
// fake platform but the real VAD and speech-to-text engines, transcribes the
// test clip through the local API (`footman transcribe`).
//
// By default it uses whisper.cpp tiny.en on the CPU, as CI does. Override
// with FOOTMAN_TEST_STT (an engine name, or "auto"), FOOTMAN_TEST_WHISPER_MODEL
// (the model, for whispercpp) and FOOTMAN_TEST_ACCEL. FOOTMAN_TEST_WAV adds
// more WAV files to time (they are not checked for accuracy), and
// FOOTMAN_TEST_CACHE keeps the GPU shader cache between runs.
func TestDaemonTranscribesWAV(t *testing.T) {
	engine := envOr("FOOTMAN_TEST_STT", "whispercpp")
	model := envOr("FOOTMAN_TEST_WHISPER_MODEL", "whisper-tiny.en")
	acc := envOr("FOOTMAN_TEST_ACCEL", "cpu")
	need := []string{"silero-vad"}
	switch engine {
	case "whispercpp":
		need = append(need, model)
	case "sherpa-parakeet":
		model = "parakeet-tdt-0.6b-v2"
		need = append(need, model)
	default:
		model = "auto"
	}
	models := speechtest.ModelsDir(t, need...)
	cache := os.Getenv("FOOTMAN_TEST_CACHE")
	if cache == "" {
		cache = t.TempDir()
	}

	startTimeout = 3 * time.Minute
	t.Cleanup(func() { startTimeout = 5 * time.Second })
	started := time.Now()
	d := startDaemonWith(t, func(o *daemonOptions) {
		o.realSpeech = true
		o.modelsDir = models
		o.cacheDir = cache
		cfg := fmt.Sprintf("[input]\nwake_word = false\n[speech.stt]\nengine = %q\nmodel = %q\naccelerator = %q\n", engine, model, acc)
		if err := os.MkdirAll(filepath.Dir(o.configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(o.configPath, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("daemon ready in %s", time.Since(started).Round(time.Millisecond))
	for _, line := range strings.Split(d.log.String(), "\n") {
		if strings.Contains(line, "speech-to-text") || strings.Contains(line, "VAD") {
			t.Log(line)
		}
	}

	transcribe := func(path string) api.TranscribeResult {
		t.Helper()
		code, out, errOut := d.cli(t, "transcribe", "--json", path)
		if code != 0 {
			t.Fatalf("footman transcribe = %d: %s\n%s", code, errOut, d.log.String())
		}
		var r api.TranscribeResult
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		var segs []string
		for _, s := range r.Segments {
			segs = append(segs, fmt.Sprintf("%d-%d ms: %.0f ms", s.StartMS, s.EndMS, s.STTMS))
		}
		t.Logf("%s (%.1f s): stt %s, vad %s; speech-to-text %.0f ms [%s], VAD %.0f ms, total %.0f ms: %q",
			filepath.Base(path), float64(r.DurationMS)/1000, r.STT, r.VAD, r.STTMS, strings.Join(segs, "; "), r.VADMS, r.TotalMS, r.Text)
		return r
	}

	clip := speechtest.ClipPath(t)
	r := transcribe(clip)
	if r.VAD != "silero" || r.STT == "" || r.STT == fakeName {
		t.Errorf("engines: stt %q, vad %q", r.STT, r.VAD)
	}
	if len(r.Segments) != 1 {
		t.Errorf("%d segments, want 1", len(r.Segments))
	}
	if w := speechtest.WER(speechtest.Reference, r.Text); w > 0.2 {
		t.Errorf("word error rate %.0f%%: %q", 100*w, r.Text)
	}
	// Timed runs, now that everything is warm.
	for range 3 {
		transcribe(clip)
	}
	for _, extra := range filepath.SplitList(os.Getenv("FOOTMAN_TEST_WAV")) {
		if extra != "" {
			for range 3 {
				transcribe(extra)
			}
		}
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// TestDaemonUnloadsAndReloadsEngines runs speech.unload_after against the
// real engines (ADR-0028): after the idle time the whisper.cpp model is
// freed and `footman status` says so; the next transcription loads it again
// and still gets the right text.
func TestDaemonUnloadsAndReloadsEngines(t *testing.T) {
	models := speechtest.ModelsDir(t, "silero-vad", "whisper-tiny.en")
	d := startDaemonWith(t, func(o *daemonOptions) {
		o.realSpeech = true
		o.modelsDir = models
		o.cacheDir = t.TempDir()
		cfg := "[input]\nwake_word = false\n[speech]\nunload_after = \"1s\"\n[speech.stt]\nengine = \"whispercpp\"\nmodel = \"whisper-tiny.en\"\naccelerator = \"cpu\"\n"
		if err := os.MkdirAll(filepath.Dir(o.configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(o.configPath, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	state := func() api.ModelState {
		t.Helper()
		_, out, _ := d.cli(t, "status", "--json")
		var s api.StatusResult
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatalf("status: %v: %s", err, out)
		}
		for _, m := range s.Models.Models {
			if m.Name == "stt" {
				return m
			}
		}
		t.Fatalf("no stt model in status: %s", out)
		return api.ModelState{}
	}
	transcribe := func() string {
		t.Helper()
		code, out, errOut := d.cli(t, "transcribe", "--json", speechtest.ClipPath(t))
		if code != 0 {
			t.Fatalf("footman transcribe = %d: %s", code, errOut)
		}
		var r api.TranscribeResult
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		return r.Text
	}
	if m := state(); !m.Loaded || m.Engine != "whispercpp" {
		t.Fatalf("at start: %+v", m)
	}
	deadline := time.Now().Add(30 * time.Second)
	for state().Loaded {
		if time.Now().After(deadline) {
			t.Fatal("stt was not unloaded after the idle time")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if w := speechtest.WER(speechtest.Reference, transcribe()); w > 0.2 {
		t.Errorf("word error rate %.0f%% after reload", 100*w)
	}
	if m := state(); m.Loads < 2 {
		t.Errorf("after use: %+v, want a reload", m)
	}
}

// TestSetupSpeechStepWithRealEngines runs the speech step of `footman setup`
// against the engines in this build, with the models already in place, so it
// must not need the network. The machine is pretended to have no GPU, so the
// CPU engine is chosen and nothing is benchmarked.
func TestSetupSpeechStepWithRealEngines(t *testing.T) {
	models := speechtest.ModelsDir(t, "silero-vad", "whisper-tiny.en")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	// No persona voice to download: the TTS engine is the fake.
	cfg := "version = 1\n[speech.stt]\nengine = \"whispercpp\"\nmodel = \"whisper-tiny.en\"\n[speech.tts]\nengine = \"fake\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var steps, recorded []string
	se := install.SpeechEnv{
		ConfigPath: cfgPath, DataDir: dir, CacheDir: filepath.Join(dir, "cache"),
		Step:   func(f string, a ...any) { steps = append(steps, fmt.Sprintf(f, a...)) },
		Record: func(e install.Entry) error { recorded = append(recorded, string(e.Kind)+" "+e.Name); return nil },
	}
	hw := accel.Hardware{RAM: 16 << 30, CPUs: 4}
	if err := runSpeechSetup(t.Context(), se, speechDirs{models: models, cache: se.CacheDir}, hw, accel.Engines(), io.Discard); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(steps, "\n")
	for _, want := range []string{"whispercpp whisper-tiny.en on cpu", "whisper-tiny.en already downloaded", "silero-vad already downloaded"} {
		if !strings.Contains(log, want) {
			t.Errorf("progress lacks %q:\n%s", want, log)
		}
	}
	if got := strings.Join(recorded, ","); !strings.Contains(got, "model whisper-tiny.en") || !strings.Contains(got, "model silero-vad") {
		t.Errorf("receipt entries: %s", got)
	}
}
