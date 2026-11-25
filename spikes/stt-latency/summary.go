package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type group struct {
	key  string
	recs []Record
}

// bucket maps a clip length to the nearest of the 5 s / 10 s / 30 s classes.
func bucket(sec float64) int {
	best, bd := 5, math.MaxFloat64
	for _, b := range []int{5, 10, 30} {
		if d := math.Abs(sec - float64(b)); d < bd {
			best, bd = b, d
		}
	}
	return best
}

func writeSummary(files []string, outPath string) error {
	var all []Record
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r Record
			if len(strings.TrimSpace(sc.Text())) == 0 {
				continue
			}
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				fh.Close()
				return fmt.Errorf("%s: %w", f, err)
			}
			all = append(all, r)
		}
		fh.Close()
	}
	byMachine := map[string]map[string][]Record{}
	for _, r := range all {
		key := fmt.Sprintf("%s|%s|%s|%d", r.Engine, r.Model, r.Accel, r.Threads)
		if byMachine[r.Label] == nil {
			byMachine[r.Label] = map[string][]Record{}
		}
		byMachine[r.Label][key] = append(byMachine[r.Label][key], r)
	}
	var b strings.Builder
	var labels []string
	for l := range byMachine {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		cpu := ""
		for _, rs := range byMachine[l] {
			cpu = rs[0].CPU
			break
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", l, cpu)
		b.WriteString("Latency is the median time from end of audio to final text (ms), by clip length. RTF is latency / clip length over all clips (lower is faster than real time). WER is total word errors / total reference words. VRAM is peak GPU memory (`own-pid` = this process, `delta-total` = growth of total GPU use, only trustworthy on an idle GPU).\n\n")
		b.WriteString("| engine | model | accel | thr | load ms | first ms | ~5 s | ~10 s | ~30 s | RTF | WER | model MB | peak RSS MB | peak VRAM MB |\n")
		b.WriteString("|---|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n")
		var keys []string
		for k := range byMachine[l] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rs := byMachine[l][k]
			by := map[int][]float64{}
			var rtf []float64
			var errs, words int
			for _, r := range rs {
				by[bucket(r.ClipSec)] = append(by[bucket(r.ClipSec)], r.LatencyMS)
				rtf = append(rtf, r.RTF)
				errs += r.Errs
				words += r.Words
			}
			cell := func(n int) string {
				if len(by[n]) == 0 {
					return "-"
				}
				return fmt.Sprintf("%.0f", median(by[n]))
			}
			vram := "-"
			if rs[0].VRAMMB > 0 {
				vram = fmt.Sprintf("%.0f (%s)", rs[0].VRAMMB, rs[0].VRAMHow)
			}
			w := 0.0
			if words > 0 {
				w = float64(errs) / float64(words) * 100
			}
			r0 := rs[0]
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %.0f | %.0f | %s | %s | %s | %.3f | %.1f%% | %.0f | %.0f | %s |\n",
				r0.Engine, r0.Model, r0.Accel, r0.Threads, r0.LoadMS, r0.FirstMS, cell(5), cell(10), cell(30),
				median(rtf), w, r0.ModelMB, r0.PeakRSSMB, vram)
		}
		b.WriteString("\n")
	}
	if outPath == "" {
		fmt.Print(b.String())
		return nil
	}
	return os.WriteFile(outPath, []byte(b.String()), 0o644)
}
