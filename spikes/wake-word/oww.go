package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// owwScores is the JSON written by py/oww_scores.py: per file, per model, one
// score per 80 ms frame (score i is available at time (i+1)*FrameS).
type owwScores struct {
	FrameS float64                         `json:"frame_s"`
	CPU    float64                         `json:"cpu_s"`
	Audio  float64                         `json:"audio_s"`
	Files  map[string]map[string][]float64 `json:"files"`
}

func loadScores(p string) owwScores {
	raw, err := os.ReadFile(p)
	must(err)
	var s owwScores
	must(json.Unmarshal(raw, &s))
	return s
}

// crossings returns rising-edge times above thr with a lockout (the product
// re-arms only after the lockout, so one utterance is one wake).
func crossings(sc []float64, frame, thr, lockout float64) []float64 {
	var out []float64
	last := -1e9
	for i, v := range sc {
		t := float64(i+1) * frame
		if v >= thr && t-last >= lockout {
			out = append(out, t)
			last = t
		}
	}
	return out
}

func cmdOWWHour(args []string) {
	fs := flag.NewFlagSet("oww-hour", flag.ExitOnError)
	scores := fs.String("scores", "data/results/oww_hour.json", "scores json")
	thrs := fs.String("thr", "0.3,0.5,0.7,0.9", "thresholds")
	fs.Parse(args)
	s := loadScores(*scores)
	for name, models := range s.Files {
		hours := s.Audio / 3600
		fmt.Printf("file %s: %.1f min; CPU %.1f s => %.2f%% of one core, %.0fx realtime\n\n", name, s.Audio/60, s.CPU, 100*s.CPU/s.Audio, s.Audio/s.CPU)
		var names []string
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		fmt.Printf("| thr | %s |\n|---|%s\n", strings.Join(names, " | "), strings.Repeat("---|", len(names)))
		for _, thr := range parseFloats(*thrs) {
			row := fmt.Sprintf("| %.2f |", thr)
			for _, m := range names {
				n := len(crossings(models[m], s.FrameS, thr, 2.0))
				row += fmt.Sprintf(" %d (%.1f/h) |", n, float64(n)/hours)
			}
			fmt.Println(row)
		}
	}
}

func cmdOWWPos(args []string) {
	fs := flag.NewFlagSet("oww-pos", flag.ExitOnError)
	scores := fs.String("scores", "data/results/oww_pos.json", "scores json")
	index := fs.String("index", "data/pos/index.json", "index.json")
	thrs := fs.String("thr", "0.3,0.5,0.7,0.9", "thresholds")
	fs.Parse(args)
	s := loadScores(*scores)
	clips, _ := loadIndex(*index)
	for _, thr := range parseFloats(*thrs) {
		res := map[string]*cell{}
		for _, c := range clips {
			models := s.Files[c.File]
			if models == nil {
				continue
			}
			key := c.Phrase + "|" + c.SNR
			ce := res[key]
			if ce == nil {
				ce = &cell{}
				res[key] = ce
			}
			ce.n++
			exp := expectOWW[c.Phrase]
			hit, wrong := false, false
			for m, sc := range models {
				for _, t := range crossings(sc, s.FrameS, thr, 2.0) {
					if t < c.Onset {
						continue
					}
					if has(exp, m) {
						if !hit {
							hit = true
							ce.lat = append(ce.lat, t-c.WakeEnd)
						}
					} else {
						wrong = true
					}
				}
			}
			if hit {
				ce.hit++
			}
			if wrong {
				ce.wrong++
			}
		}
		fmt.Printf("\n### openWakeWord, threshold %.2f\n\nWake word detection (hit%% / wrong-model%% ; latency ms mean/p95 after word end; \"-\" rows are phrases with no ready-made OWW model, so every hit is a false fire)\n\n", thr)
		fmt.Printf("| phrase | %s |\n|---|%s\n", strings.Join(snrOrder, " | "), strings.Repeat("---|", len(snrOrder)))
		for _, ph := range []string{"alexa", "hey_jarvis", "jarvis", "hey_mycroft", "alfred", "hey_footman", "alfred_stop", "alfred_cancel", "stop", "cancel"} {
			row := "| " + ph + " |"
			for _, sn := range snrOrder {
				c := res[ph+"|"+sn]
				if c == nil {
					row += " |"
					continue
				}
				row += fmt.Sprintf(" %.0f%% / %.0f%% ; %s |", 100*float64(c.hit)/float64(c.n), 100*float64(c.wrong)/float64(c.n), c.latStr(c.lat))
			}
			fmt.Println(row)
		}
	}
	_ = filepath.Join
}
