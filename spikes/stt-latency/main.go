// stt-bench measures speech-to-text latency for one engine, model and
// accelerator per process, so peak RSS is that combination's own. It writes one
// JSON line per clip; `stt-bench -summarize a.jsonl b.jsonl -md out.md` folds
// result files into a Markdown table. Spike code: throwaway, never imported.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Record is one JSONL line: one clip, with the run-wide context repeated so a
// line stands alone.
type Record struct {
	Time      string    `json:"time"`
	Label     string    `json:"label"` // machine label, e.g. workstation, zenbook
	OS        string    `json:"os"`
	CPU       string    `json:"cpu"`
	Engine    string    `json:"engine"`
	Model     string    `json:"model"`
	Accel     string    `json:"accel"`
	Threads   int       `json:"threads"`
	Backend   string    `json:"backend"`
	ModelMB   float64   `json:"model_mb"`
	LoadMS    float64   `json:"load_ms"`
	FirstMS   float64   `json:"first_run_ms"` // warm-up decode, includes lazy GPU init
	RSSLoadMB float64   `json:"rss_after_load_mb"`
	PeakRSSMB float64   `json:"peak_rss_mb"`
	VRAMMB    float64   `json:"peak_vram_mb"`
	VRAMHow   string    `json:"vram_method,omitempty"`
	Clip      string    `json:"clip"`
	ClipSec   float64   `json:"clip_sec"`
	RunsMS    []float64 `json:"runs_ms"`
	LatencyMS float64   `json:"latency_ms"` // median: end of audio to final text
	InnerMS   float64   `json:"inner_ms,omitempty"`
	RTF       float64   `json:"rtf"`
	WER       float64   `json:"wer"`
	Errs      int       `json:"word_errors"`
	Words     int       `json:"ref_words"`
	Hyp       string    `json:"hyp"`
	Ref       string    `json:"ref"`
}

func main() {
	var (
		engine    = flag.String("engine", "", "engine: sherpa | whispercpp | whispercli")
		model     = flag.String("model", "", "model id, e.g. whisper-base.en (see -list-models)")
		accel     = flag.String("accel", "cpu", "accelerator: cpu | cuda | vulkan | directml")
		threads   = flag.Int("threads", 4, "CPU threads")
		runs      = flag.Int("runs", 3, "timed runs per clip (median reported)")
		clipsDir  = flag.String("clips", "clips", "directory of 16 kHz mono .wav clips with .txt references")
		maxClips  = flag.Int("max-clips", 0, "only use the first N clips (0 = all)")
		match     = flag.String("clip", "", "only clips whose name contains this")
		modelsDir = flag.String("models", defaultModels(), "models directory")
		out       = flag.String("out", "", "append JSON lines here (default: stdout only)")
		label     = flag.String("label", "", "machine label stored in results (default: os/arch)")
		variant   = flag.String("variant", "auto", "sherpa weights: int8 | fp32 | auto")
		cli       = flag.String("whisper-cli", "", "whispercli engine: path to whisper-cli(.exe)")
		doDetect  = flag.Bool("detect", false, "print detected hardware and compiled engines, then exit")
		listModel = flag.Bool("list-models", false, "list model ids and exit")
		summarize = flag.Bool("summarize", false, "treat the arguments as .jsonl files and write a Markdown summary")
		md        = flag.String("md", "", "with -summarize: write Markdown here (default: stdout)")
	)
	flag.Parse()

	switch {
	case *doDetect:
		printJSON(detect())
		return
	case *listModel:
		for _, m := range modelSpecs {
			fmt.Printf("%-24s sherpa=%-4v whispercpp=%v\n", m.ID, m.Sherpa != "", m.GGML != "")
		}
		return
	case *summarize:
		// flag stops at the first file name, so pick up flags that follow it.
		var files []string
		for flag.NArg() > 0 {
			files = append(files, flag.Arg(0))
			flag.CommandLine.Parse(flag.Args()[1:])
		}
		if err := writeSummary(files, *md); err != nil {
			fatal(err)
		}
		return
	}

	e, ok := engines[*engine]
	if !ok {
		fatal(fmt.Errorf("engine %q is not in this build; have: %s", *engine, engineNames()))
	}
	spec, err := findModel(*model)
	if err != nil {
		fatal(err)
	}
	clips, err := loadClips(*clipsDir)
	if err != nil {
		fatal(err)
	}
	if *match != "" {
		var k []*Clip
		for _, c := range clips {
			if strings.Contains(c.ID, *match) {
				k = append(k, c)
			}
		}
		clips = k
	}
	if *maxClips > 0 && len(clips) > *maxClips {
		clips = clips[:*maxClips]
	}
	if len(clips) == 0 {
		fatal(fmt.Errorf("no clips selected"))
	}

	cpu, _ := cpuInfo()
	lbl := *label
	if lbl == "" {
		lbl = runtime.GOOS + "-" + runtime.GOARCH
	}
	eng := e.New()
	opts := LoadOpts{Model: spec, ModelsDir: *modelsDir, Accel: *accel, Threads: *threads, Variant: *variant, CLIPath: *cli}

	vram := startVRAM(*accel)
	t0 := time.Now()
	if err := eng.Load(opts); err != nil {
		fatal(err)
	}
	load := time.Since(t0)
	defer eng.Close()
	rssLoad, _ := rssMB()

	// Warm-up: the first decode pays for lazy initialisation (GPU kernels,
	// allocator growth), which a long-running assistant pays once, not per
	// utterance. Reported separately as first_run_ms.
	t0 = time.Now()
	if _, err := eng.Transcribe(clips[0]); err != nil {
		fatal(fmt.Errorf("warm-up: %w", err))
	}
	first := time.Since(t0)

	var recs []Record
	for _, c := range clips {
		var lat, inner []float64
		var text string
		for i := 0; i < *runs; i++ {
			t := time.Now()
			r, err := eng.Transcribe(c)
			d := time.Since(t)
			if err != nil {
				fatal(fmt.Errorf("%s: %w", c.ID, err))
			}
			if r.Latency > 0 {
				d = r.Latency
			}
			lat = append(lat, ms(d))
			if r.Inner > 0 {
				inner = append(inner, ms(r.Inner))
			}
			text = r.Text
		}
		errs, words := werCounts(c.Ref, text)
		m := median(lat)
		recs = append(recs, Record{
			Time: time.Now().UTC().Format(time.RFC3339), Label: lbl, OS: runtime.GOOS, CPU: cpu,
			Engine: *engine, Model: spec.ID, Accel: *accel, Threads: *threads, Backend: eng.Info(),
			LoadMS: ms(load), FirstMS: ms(first), RSSLoadMB: rssLoad,
			Clip: c.ID, ClipSec: c.Seconds(), RunsMS: lat, LatencyMS: m, InnerMS: median(inner),
			RTF: m / 1000 / c.Seconds(), WER: wer(c.Ref, text), Errs: errs, Words: words, Hyp: text, Ref: c.Ref,
		})
		fmt.Fprintf(os.Stderr, "%-22s %5.1fs  latency %8.1f ms  rtf %.3f  wer %.3f\n", c.ID, c.Seconds(), m, m/1000/c.Seconds(), wer(c.Ref, text))
	}
	peakVRAM, how := vram.Stop()
	_, peak := rssMB()
	size := float64(eng.ModelBytes()) / 1048576

	var w *bufio.Writer
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fatal(err)
		}
		defer f.Close()
		w = bufio.NewWriter(f)
		defer w.Flush()
	}
	for i := range recs {
		recs[i].PeakRSSMB, recs[i].VRAMMB, recs[i].VRAMHow, recs[i].ModelMB = peak, peakVRAM, how, size
		b, _ := json.Marshal(recs[i])
		if w != nil {
			w.Write(append(b, '\n'))
		} else {
			fmt.Println(string(b))
		}
	}
	if how == "" {
		how = "n/a"
	}
	fmt.Fprintf(os.Stderr, "load %.0f ms, first run %.0f ms, peak RSS %.0f MB, peak VRAM %.0f MB (%s), model %.0f MB\n",
		ms(load), ms(first), peak, peakVRAM, how, size)
}

func defaultModels() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "footman-spike", "models")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "stt-bench:", err)
	os.Exit(1)
}
