// Package fake provides a deterministic, scriptable worker for tests.
// Importing it registers the worker as "fake" in jobs.Workers:
//
//	w, _ := jobs.Workers.New("fake", registry.Map{"summaries": []string{"Tests pass."}})
//	w.(*fake.Worker).Queue(fake.Outcome{Hold: true}) // runs until Release or Cancel
package fake

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/registry"
)

// Name is the registry name of the fake worker.
const Name = "fake"

func init() {
	jobs.Workers.Register(Name, func(cfg registry.Config) (jobs.Worker, error) { return New(cfg) })
}

// ErrNoOutcome is returned by Start when no outcome is queued.
var ErrNoOutcome = errors.New("fake worker: no outcome queued")

// Outcome scripts one job.
type Outcome struct {
	// Progress messages are sent as Running updates, in order.
	Progress []string
	// Result is returned by Wait if the job succeeds.
	Result jobs.Result
	// Err, if set, makes the job fail with it.
	Err error
	// Hold keeps the job Running after its progress until Job.Release,
	// Job.Cancel or the Start context ends.
	Hold bool
}

// Worker runs queued outcomes in order, one per Start call, and records every
// job it started.
type Worker struct {
	mu    sync.Mutex
	queue []Outcome
	jobs  []*Job
}

// New builds a Worker from cfg (key "summaries": a list of result summaries,
// each queued as an outcome that succeeds at once).
func New(cfg registry.Config) (*Worker, error) {
	var opts struct {
		Summaries []string `json:"summaries"`
	}
	if err := cfg.Decode(&opts); err != nil {
		return nil, err
	}
	w := &Worker{}
	for _, s := range opts.Summaries {
		w.Queue(Outcome{Result: jobs.Result{Summary: s}})
	}
	return w, nil
}

// Queue appends outcomes for later Start calls.
func (w *Worker) Queue(os ...Outcome) {
	w.mu.Lock()
	w.queue = append(w.queue, os...)
	w.mu.Unlock()
}

// Jobs returns every job started so far, in order.
func (w *Worker) Jobs() []*Job {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*Job(nil), w.jobs...)
}

// Start implements jobs.Worker.
func (w *Worker) Start(ctx context.Context, t jobs.Task) (jobs.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return nil, ErrNoOutcome
	}
	o := w.queue[0]
	w.queue = w.queue[1:]

	ctx, cancel := context.WithCancel(ctx)
	j := &Job{
		task:    t,
		status:  jobs.Running,
		updates: make(chan jobs.Update, len(o.Progress)+2),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		cancel:  cancel,
	}
	w.jobs = append(w.jobs, j)
	go j.run(ctx, o)
	return j, nil
}

// Job is a job started by the fake Worker.
type Job struct {
	task    jobs.Task
	updates chan jobs.Update
	release chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	once    sync.Once

	mu     sync.Mutex
	status jobs.Status
	result jobs.Result
	err    error
}

func (j *Job) run(ctx context.Context, o Outcome) {
	defer j.cancel()
	j.send(jobs.Running, "started")
	for _, p := range o.Progress {
		j.send(jobs.Running, p)
	}
	if o.Hold {
		select {
		case <-j.release:
		case <-ctx.Done():
		}
	}
	j.mu.Lock()
	switch {
	case ctx.Err() != nil:
		j.status, j.err = jobs.Cancelled, context.Canceled
	case o.Err != nil:
		j.status, j.err = jobs.Failed, o.Err
	default:
		j.status, j.result = jobs.Succeeded, o.Result
	}
	final, msg := j.status, j.result.Summary
	if j.err != nil {
		msg = j.err.Error()
	}
	j.mu.Unlock()
	j.updates <- jobs.Update{Status: final, Message: msg, At: time.Now()}
	close(j.updates)
	close(j.done)
}

func (j *Job) send(s jobs.Status, msg string) {
	j.updates <- jobs.Update{Status: s, Message: msg, At: time.Now()}
}

// Task returns the task the job was started with.
func (j *Job) Task() jobs.Task { return j.task }

// Release lets a held job finish with its scripted outcome.
func (j *Job) Release() { j.once.Do(func() { close(j.release) }) }

// ID implements jobs.Job.
func (j *Job) ID() string { return j.task.ID }

// Status implements jobs.Job.
func (j *Job) Status() jobs.Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// Updates implements jobs.Job.
func (j *Job) Updates() <-chan jobs.Update { return j.updates }

// Wait implements jobs.Job.
func (j *Job) Wait(ctx context.Context) (jobs.Result, error) {
	select {
	case <-j.done:
	case <-ctx.Done():
		return jobs.Result{}, ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.err
}

// Cancel implements jobs.Job.
func (j *Job) Cancel() { j.cancel() }
