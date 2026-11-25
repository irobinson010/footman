// Package jobs defines the Worker and Job interfaces for long-running
// background work, and the by-name worker registry. The job manager (queue,
// concurrency limit, persistence) will live here too; workers (built-in,
// Claude Code, Codex) live in subpackages and register themselves. fake holds
// a scriptable worker for tests.
package jobs
