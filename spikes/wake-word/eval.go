package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Clip is one synthetic test clip (see py/synth.py).
type Clip struct {
	File    string  `json:"file"`
	Phrase  string  `json:"phrase"`
	Voice   string  `json:"voice"`
	SNR     string  `json:"snr"`
	Onset   float64 `json:"onset"`
	End     float64 `json:"end"`
	WakeEnd float64 `json:"wake_end"`
}

func loadIndex(path string) ([]Clip, string) {
	raw, err := os.ReadFile(path)
	must(err)
	var c []Clip
	must(json.Unmarshal(raw, &c))
	return c, filepath.Dir(filepath.Dir(path)) // data dir: index is data/pos/index.json
}

var snrOrder = []string{"clean", "snr20", "snr10", "snr5", "snr0"}

// expectSherpa: which sherpa wake keywords count as a correct detection for a phrase.
var expectSherpa = map[string][]string{
	"alfred": {"ALFRED"}, "jarvis": {"JARVIS", "HEY_JARVIS"}, "hey_footman": {"HEY_FOOTMAN"},
	"hey_jarvis": {"HEY_JARVIS", "JARVIS"}, "alexa": {"ALEXA"}, "hey_mycroft": {"HEY_MYCROFT"},
	"alfred_stop": {"ALFRED"}, "alfred_cancel": {"ALFRED"},
}

// expectOWW: openWakeWord ready-made models that should fire.
var expectOWW = map[string][]string{
	"alexa": {"alexa"}, "hey_jarvis": {"hey_jarvis"}, "jarvis": {"hey_jarvis"}, "hey_mycroft": {"hey_mycroft"},
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// cell aggregates results for one (phrase, snr).
type cell struct {
	n, hit, wrong int // wrong: a keyword that should not fire did
	lat           []float64
	bgN, bgHit    int
	bgWrong       int
	bgLat         []float64
	rawHit        int
}

func (c *cell) latStr(l []float64) string {
	if len(l) == 0 {
		return "-"
	}
	sort.Float64s(l)
	m := 0.0
	for _, v := range l {
		m += v
	}
	m /= float64(len(l))
	p95 := l[int(math.Min(float64(len(l)-1), math.Ceil(0.95*float64(len(l)))-1))]
	return fmt.Sprintf("%.0f/%.0f", m*1000, p95*1000)
}

func cmdPos(args []string) {
	fs := flag.NewFlagSet("pos", flag.ExitOnError)
	index := fs.String("index", "data/pos/index.json", "index.json")
	thrs := fs.String("thr", "0.1,0.25,0.4,0.6", "wake thresholds")
	s2thr := fs.Float64("s2thr", 0.25, "stage-2 threshold")
	s2boost := fs.Float64("s2boost", 1.0, "stage-2 boost")
	boost := fs.String("boost", "1.0", "boost score(s), one per -thr or a single value")
	fs.Parse(args)
	clips, dataDir := loadIndex(*index)
	ths := parseFloats(*thrs)
	bs := broadcast(parseFloats(*boost), len(ths))
	for ti, thr := range ths {
		p := NewPipeline(thr, bs[ti], *s2thr, *s2boost, true)
		res := map[string]*cell{}
		for _, c := range clips {
			x := readWav(filepath.Join(dataDir, c.File))
			p.Reset()
			ev := p.Run(x)
			key := c.Phrase + "|" + c.SNR
			ce := res[key]
			if ce == nil {
				ce = &cell{}
				res[key] = ce
			}
			ce.n++
			exp := expectSherpa[c.Phrase]
			hit, wrong := false, false
			var lat float64
			for _, e := range ev {
				if e.Kind != "wake" || e.T < c.Onset {
					continue
				}
				if has(exp, e.Name) {
					if !hit {
						hit, lat = true, e.T-c.WakeEnd
					}
				} else {
					wrong = true
				}
			}
			if hit {
				ce.hit++
				ce.lat = append(ce.lat, lat)
			}
			if wrong {
				ce.wrong++
			}
			// break-glass bookkeeping
			var wantBG string
			switch c.Phrase {
			case "alfred_stop", "stop":
				wantBG = "STOP_EVERYTHING"
			case "alfred_cancel", "cancel":
				wantBG = "CANCEL_EVERYTHING"
			}
			if wantBG != "" {
				ce.bgN++
				ok, bad, raw := false, false, false
				for _, e := range ev {
					switch e.Kind {
					case "bg":
						if e.Name == wantBG && (c.Phrase == "alfred_stop" || c.Phrase == "alfred_cancel") {
							if !ok {
								ok = true
								ce.bgLat = append(ce.bgLat, e.T-c.End)
							}
						} else {
							bad = true
						}
					case "raw":
						if e.Name == wantBG && e.T >= c.Onset {
							raw = true
						}
					}
				}
				if raw {
					ce.rawHit++
				}
				if ok {
					ce.bgHit++
				}
				if bad {
					ce.bgWrong++
				}
			}
		}
		printPos(fmt.Sprintf("sherpa-onnx KWS, wake boost %g thr %g (stage-2 boost %g thr %g)", bs[ti], thr, *s2boost, *s2thr), res)
		p.Close()
	}
}

func printPos(title string, res map[string]*cell) {
	fmt.Printf("\n### %s\n\nWake word detection (hit%% / wrong-keyword%% ; latency ms mean/p95 after word end)\n\n", title)
	fmt.Printf("| phrase | %s |\n|---|%s\n", strings.Join(snrOrder, " | "), strings.Repeat("---|", len(snrOrder)))
	phrases := []string{"alfred", "jarvis", "hey_footman", "hey_jarvis", "alexa", "hey_mycroft", "alfred_stop", "alfred_cancel", "stop", "cancel"}
	for _, ph := range phrases {
		row := "| " + ph + " |"
		any := false
		for _, s := range snrOrder {
			c := res[ph+"|"+s]
			if c == nil {
				row += " |"
				continue
			}
			any = true
			row += fmt.Sprintf(" %.0f%% / %.0f%% ; %s |", 100*float64(c.hit)/float64(c.n), 100*float64(c.wrong)/float64(c.n), c.latStr(c.lat))
		}
		if any {
			fmt.Println(row)
		}
	}
	fmt.Printf("\nBreak-glass (wake word, then stage 2). `fire%%` = gated break-glass fired with the right phrase; `bad%%` = fired wrongly; `raw%%` = ungated stage-2 spotter alone heard the phrase; latency ms after end of speech mean/p95\n\n")
	fmt.Printf("| phrase | %s |\n|---|%s\n", strings.Join(snrOrder, " | "), strings.Repeat("---|", len(snrOrder)))
	for _, ph := range []string{"alfred_stop", "alfred_cancel", "stop", "cancel"} {
		row := "| " + ph + " |"
		for _, s := range snrOrder {
			c := res[ph+"|"+s]
			if c == nil {
				row += " |"
				continue
			}
			row += fmt.Sprintf(" fire %.0f%% bad %.0f%% raw %.0f%% ; %s |", 100*float64(c.bgHit)/float64(c.bgN), 100*float64(c.bgWrong)/float64(c.bgN), 100*float64(c.rawHit)/float64(c.bgN), c.latStr(c.bgLat))
		}
		fmt.Println(row)
	}
}
