package core

import (
	"errors"
	"fmt"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/jobs"
)

// StartJob hands t to the worker and returns its ID at once. The job runs
// under the pipeline's lifetime, not the request's, is cancelled by Stop and
// Close, and publishes its progress on JobUpdated. A job manager with limits
// and queueing (design §19) will take this over.
func (p *Pipeline) StartJob(t jobs.Task) (string, error) {
	if p.d.Worker == nil {
		return "", errors.New("core: no worker configured")
	}
	// Hold the lock across Start (which returns at once) so a concurrent
	// Stop either sees the job or happens before it starts.
	p.mu.Lock()
	p.jobSeq++
	t.ID = fmt.Sprintf("job-%d", p.jobSeq)
	j, err := p.d.Worker.Start(p.ctx, t)
	if err != nil {
		p.mu.Unlock()
		return "", err
	}
	p.jobs[t.ID] = j
	p.mu.Unlock()
	p.wg.Go(func() { p.watchJob(j, t) })
	return t.ID, nil
}

func (p *Pipeline) watchJob(j jobs.Job, t jobs.Task) {
	for u := range j.Updates() {
		bus.Publish(p.d.Bus, JobUpdated, JobEvent{ID: t.ID, Prompt: t.Prompt, Update: u})
	}
	p.mu.Lock()
	delete(p.jobs, t.ID)
	p.mu.Unlock()
}

// Job returns the running job with the given ID, or nil.
func (p *Pipeline) Job(id string) jobs.Job {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobs[id]
}
