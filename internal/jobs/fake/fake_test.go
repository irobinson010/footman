package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/jobs/fake"
	"github.com/irobinson010/footman/internal/registry"
)

func TestWorker(t *testing.T) {
	w, err := jobs.Workers.New(fake.Name, registry.Map{"summaries": []string{"Tests pass."}})
	if err != nil {
		t.Fatal(err)
	}
	fw := w.(*fake.Worker)
	boom := errors.New("boom")
	fw.Queue(
		fake.Outcome{Progress: []string{"thinking"}, Err: boom},
		fake.Outcome{Hold: true},
		fake.Outcome{Hold: true, Result: jobs.Result{Summary: "released"}},
	)
	ctx := context.Background()

	// Succeeds at once.
	j, err := w.Start(ctx, jobs.Task{ID: "1", Prompt: "run the tests"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := j.Wait(ctx)
	if err != nil || r.Summary != "Tests pass." || j.Status() != jobs.Succeeded || j.ID() != "1" {
		t.Fatalf("job 1: %+v, %v, %v", r, err, j.Status())
	}
	var last jobs.Update
	n := 0
	for u := range j.Updates() {
		last, n = u, n+1
	}
	if n != 2 || last.Status != jobs.Succeeded || !last.Status.Final() {
		t.Fatalf("job 1 updates: %d, last %+v", n, last)
	}

	// Fails after progress.
	j, _ = w.Start(ctx, jobs.Task{ID: "2"})
	if _, err := j.Wait(ctx); !errors.Is(err, boom) || j.Status() != jobs.Failed {
		t.Fatalf("job 2: %v, %v", err, j.Status())
	}

	// Held, then cancelled.
	j, _ = w.Start(ctx, jobs.Task{ID: "3"})
	if j.Status() != jobs.Running {
		t.Fatalf("held job status = %v", j.Status())
	}
	j.Cancel()
	if _, err := j.Wait(ctx); !errors.Is(err, context.Canceled) || j.Status() != jobs.Cancelled {
		t.Fatalf("job 3: %v, %v", err, j.Status())
	}

	// Held, then released.
	j, _ = w.Start(ctx, jobs.Task{ID: "4"})
	j.(*fake.Job).Release()
	if r, err := j.Wait(ctx); err != nil || r.Summary != "released" {
		t.Fatalf("job 4: %+v, %v", r, err)
	}

	if _, err := w.Start(ctx, jobs.Task{}); !errors.Is(err, fake.ErrNoOutcome) {
		t.Fatalf("empty queue err = %v", err)
	}
	if len(fw.Jobs()) != 4 {
		t.Fatalf("recorded %d jobs", len(fw.Jobs()))
	}
}
