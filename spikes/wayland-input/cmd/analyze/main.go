// analyze reads a sink log (JSON lines {"t": ms, "text": ...}) and compares it
// with the expected text: did it arrive intact, and how fast.
//
//	analyze -log qt.jsonl -expect-file sample.txt [-since MS]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

type rec struct {
	T    float64 `json:"t"`
	RT   float64 `json:"rt"` // websink: server receive time
	Seq  int     `json:"seq"`
	Text string  `json:"text"`
	Raw  string  `json:"raw"`
}

func main() {
	logPath := flag.String("log", "", "sink log")
	expect := flag.String("expect", "", "expected text")
	expectFile := flag.String("expect-file", "", "expected text file")
	since := flag.Float64("since", 0, "ignore records before this unix ms")
	flag.Parse()
	want := *expect
	if *expectFile != "" {
		b, err := os.ReadFile(*expectFile)
		if err != nil {
			panic(err)
		}
		want = string(b)
	}
	f, err := os.Open(*logPath)
	if err != nil {
		panic(err)
	}
	var recs []rec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r rec
		if json.Unmarshal([]byte(line), &r) == nil && max(r.T, r.RT) >= *since {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		fmt.Println("no records")
		os.Exit(1)
	}
	// Browsers post asynchronously; order by timestamp, not by log position.
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].Seq != recs[j].Seq {
			return recs[i].Seq < recs[j].Seq
		}
		return recs[i].T < recs[j].T
	})
	got := recs[len(recs)-1].Text
	// Terminals send Enter as \r; normalise for comparison.
	norm := func(s string) string { return strings.ReplaceAll(s, "\r", "\n") }
	first, last := recs[0].T, recs[len(recs)-1].T
	n := utf8.RuneCountInString(want)
	ok := norm(got) == norm(want)
	dur := (last - first) / 1000
	cps := 0.0
	if dur > 0 {
		cps = float64(n) / dur
	}
	bracketed := false
	for _, r := range recs {
		if strings.Contains(r.Raw, "\x1b[200~") {
			bracketed = true
		}
	}
	lag := 0.0
	if *since > 0 {
		lag = (first - *since) / 1000
	}
	fmt.Printf("match=%v chars=%d events=%d first_after=%.3fs span=%.3fs cps=%.0f bracketed_paste=%v\n", ok, n, len(recs), lag, dur, cps, bracketed)
	if !ok {
		fmt.Printf("  want %q\n  got  %q\n", trunc(want), trunc(got))
	}
}

func trunc(s string) string {
	if utf8.RuneCountInString(s) > 160 {
		r := []rune(s)
		return string(r[:80]) + " … " + string(r[len(r)-60:])
	}
	return s
}
