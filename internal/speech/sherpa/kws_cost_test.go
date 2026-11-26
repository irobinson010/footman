//go:build native && cgo

package sherpa

import (
	"slices"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/speech/speechtest"
)

// TestKWSChunkCost reports what one 100 ms chunk costs on the CPU while
// speech is playing (the worst case; silence is cheaper).
func TestKWSChunkCost(t *testing.T) {
	k := openKWS(t)
	d := newDetector(k)
	defer d.close()
	x := loadClip(t, speechtest.Clip10Path(t)).Samples
	var cost []time.Duration
	for i := 0; i+chunkSamples <= len(x); i += chunkSamples {
		start := time.Now()
		d.feed(x[i : i+chunkSamples])
		cost = append(cost, time.Since(start))
	}
	slices.Sort(cost)
	var sum time.Duration
	for _, c := range cost {
		sum += c
	}
	n := len(cost)
	mean, p95, worst := sum/time.Duration(n), cost[n*95/100], cost[n-1]
	t.Logf("%d chunks of 100 ms: mean %s, p95 %s, max %s (%.1f%% of one core in real time)", n,
		mean.Round(time.Microsecond), p95.Round(time.Microsecond), worst.Round(time.Microsecond),
		100*float64(sum)/float64(n*int(100*time.Millisecond)))
	if p95 > 50*time.Millisecond {
		t.Errorf("p95 %s per 100 ms chunk is too slow", p95)
	}
}
