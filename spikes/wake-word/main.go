// Command wakeword is the S0-08 spike harness: it runs sherpa-onnx keyword
// spotting (and Silero VAD) over audio files, applies a two-stage break-glass
// gate, and scores openWakeWord score timelines produced by py/oww_scores.py.
//
// Throwaway code. Nothing in internal/ or cmd/ imports it.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const usage = `usage: wakeword <command> [flags]

  kws      -wav f.wav [-thr 0.25 -boost 1.0]   print wake/break-glass detections with timestamps
  vad      -wav f.wav                           print Silero VAD speech segments
  hour     -wav hour.wav -thr 0.1,0.25,0.4     false wakes per hour + CPU for sherpa KWS
  pos      -index data/pos/index.json -thr ... detection rate/latency on synthetic positives
  vadtest  -index data/pos/index.json          VAD onset latency / recall on synthetic positives
  oww-hour -scores s.json -thr 0.3,0.5,...     false wakes per hour from openWakeWord scores
  oww-pos  -scores s.json -index ... -thr ...  detection rate/latency from openWakeWord scores
  listen   [-thr 0.25]                         read raw f32le 16 kHz mono from stdin (owner live test)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "kws":
		cmdKWS(args)
	case "vad":
		cmdVAD(args)
	case "hour":
		cmdHour(args)
	case "pos":
		cmdPos(args)
	case "vadtest":
		cmdVADTest(args)
	case "oww-hour":
		cmdOWWHour(args)
	case "oww-pos":
		cmdOWWPos(args)
	case "listen":
		cmdListen(args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func parseFloats(s string) []float64 {
	var out []float64
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad number:", p)
			os.Exit(2)
		}
		out = append(out, v)
	}
	return out
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func broadcast(v []float64, n int) []float64 {
	if len(v) == 1 {
		for len(v) < n {
			v = append(v, v[0])
		}
	}
	if len(v) != n {
		must(fmt.Errorf("-boost needs 1 or %d values", n))
	}
	return v
}
