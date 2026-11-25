package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/irobinson010/footman/internal/registry"
)

// Task is a piece of background work handed to a worker.
type Task struct {
	// ID is assigned by the job manager and identifies the job everywhere
	// (store, API, notifications).
	ID string
	// Prompt says what to do, in natural language.
	Prompt string
	// Dir is the project directory to work in, if any. Code-edit workers run
	// in a new branch or worktree of it (design §19).
	Dir string
	// Untrusted marks a task whose context already contains untrusted content
	// (design §29), so outward and destructive actions need confirmation.
	Untrusted bool
}

// Status is where a job is in its life.
type Status int

// Job statuses. Succeeded, Failed and Cancelled are final.
const (
	// Queued means the job is waiting for a free slot.
	Queued Status = iota + 1
	// Running means the worker is on it.
	Running
	// Succeeded means it finished and Result is valid.
	Succeeded
	// Failed means it stopped with an error.
	Failed
	// Cancelled means it was cancelled before finishing.
	Cancelled
)

var statusNames = [...]string{Queued: "queued", Running: "running", Succeeded: "succeeded", Failed: "failed", Cancelled: "cancelled"}

// String returns the lower-case name of s.
func (s Status) String() string {
	if s >= Queued && s <= Cancelled {
		return statusNames[s]
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Final reports whether s is Succeeded, Failed or Cancelled.
func (s Status) Final() bool { return s >= Succeeded && s <= Cancelled }

// Update is a progress report from a running job.
type Update struct {
	// Status is the job's status after this update.
	Status Status
	// Message is a short human-readable line, suitable for speaking.
	Message string
	// At is when the update happened.
	At time.Time
}

// Result is what a finished job produced.
type Result struct {
	// Summary is a short description of the outcome, suitable for speaking.
	Summary string
	// Output is the worker's full output, for the UI and logs.
	Output string
	// Diff is the change made by a code-edit job, shown before it finishes.
	Diff string
}

// Job is a running piece of background work.
type Job interface {
	// ID returns the task ID.
	ID() string
	// Status returns the current status.
	Status() Status
	// Updates returns a channel of progress updates. It is closed after the
	// update with a final status. Every call returns the same channel.
	Updates() <-chan Update
	// Wait blocks until the job reaches a final status or ctx is done. It
	// returns the result, or the job's error (context.Canceled after
	// Cancel), or ctx's error if ctx ends first.
	Wait(ctx context.Context) (Result, error)
	// Cancel asks the job to stop. It does not wait; use Wait for that. It is
	// safe to call more than once and after the job has finished.
	Cancel()
}

// Worker runs tasks in the background.
type Worker interface {
	// Start begins t and returns at once. The job runs until it finishes, is
	// cancelled with Job.Cancel, or ctx is done; ctx therefore must outlive
	// the job (the job manager passes its own context, not a request's).
	Start(ctx context.Context, t Task) (Job, error)
}

// Workers holds the workers, keyed by the name used in config
// (`[jobs] worker = "claude-code"`).
var Workers = registry.New[Worker]("worker")
