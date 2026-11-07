// Package jobs runs long operations (exports, imports, copies) in the
// background so the caller gets an immediate reply and polls for progress.
// Jobs live in memory only: they are lost if the agent restarts, and a job
// still running at shutdown is cancelled.
package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Job states.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Progress is a best-effort progress report. Total is an estimate when the
// underlying tool does not report one, so Percent is only indicative.
type Progress struct {
	BytesDone  int64   `json:"bytes_done"`
	BytesTotal int64   `json:"bytes_total,omitempty"`
	Percent    float64 `json:"percent,omitempty"`
	Note       string  `json:"note,omitempty"`
}

// Job is a snapshot of one background job, safe to marshal.
type Job struct {
	ID         string    `json:"job_id"`
	Operation  string    `json:"operation"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	ElapsedS   float64   `json:"elapsed_s"`
	Progress   Progress  `json:"progress"`
	// Result is the operation's own output once finished (successfully or not).
	Result any `json:"result,omitempty"`
	// Error is set when the job failed or was cancelled.
	Error string `json:"error,omitempty"`
}

type job struct {
	mu     sync.Mutex
	data   Job
	cancel context.CancelFunc
}

func (j *job) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.data
	end := s.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	s.ElapsedS = end.Sub(s.StartedAt).Seconds()
	return s
}

// Func is the work a job performs. It must stop promptly when ctx is cancelled.
// The returned failed flag marks a logical failure (the operation ran but did
// not succeed) even when err is nil; msg then describes it.
type Func func(ctx context.Context) (result any, failed bool, msg string, err error)

// Manager owns the jobs.
type Manager struct {
	logger  *zap.Logger
	maxRun  int
	keep    int
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	jobs    map[string]*job
	running int
}

// ErrTooManyJobs is returned by Start when the concurrency limit is reached.
var ErrTooManyJobs = errors.New("too many background jobs running")

// New creates a Manager allowing maxRunning concurrent jobs and remembering
// the keep most recent finished ones.
func New(logger *zap.Logger, maxRunning, keep int) *Manager {
	if maxRunning < 1 {
		maxRunning = 2
	}
	if keep < 1 {
		keep = 100
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{logger: logger, maxRun: maxRunning, keep: keep, ctx: ctx, cancel: cancel, jobs: map[string]*job{}}
}

type ctxKey struct{}

// Report updates the progress of the job whose context this is. It is a no-op
// outside a job, so module code can call it unconditionally.
func Report(ctx context.Context, done, total int64, note string) {
	j, ok := ctx.Value(ctxKey{}).(*job)
	if !ok {
		return
	}
	p := Progress{BytesDone: done, BytesTotal: total, Note: note}
	if total > 0 && done > total {
		// The estimate was too low; claiming a percentage would be wrong.
		p.BytesTotal = 0
		p.Note += " [size estimate exceeded; percent unknown]"
	} else if total > 0 {
		p.Percent = float64(done) * 100 / float64(total)
		if p.Percent > 99 {
			p.Percent = 99 // never claim 100 before the job really finishes
		}
	}
	j.mu.Lock()
	j.data.Progress = p
	j.mu.Unlock()
}

// InJob reports whether ctx belongs to a background job.
func InJob(ctx context.Context) bool {
	_, ok := ctx.Value(ctxKey{}).(*job)
	return ok
}

// Start launches fn in the background and returns the new job's snapshot.
func (m *Manager) Start(operation string, fn Func) (Job, error) {
	return m.StartWith(operation, fn, nil)
}

// StartWith is Start with a hook called once with the finished job (after its
// slot is released), for example to notify a caller.
func (m *Manager) StartWith(operation string, fn Func, onFinish func(Job)) (Job, error) {
	m.mu.Lock()
	if m.running >= m.maxRun {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("%w (limit %d); wait for one to finish or cancel it", ErrTooManyJobs, m.maxRun)
	}
	m.running++
	j := &job{data: Job{ID: newID(), Operation: operation, Status: StatusRunning, StartedAt: time.Now()}}
	ctx, cancel := context.WithCancel(m.ctx)
	j.cancel = cancel
	m.jobs[j.data.ID] = j
	m.evictLocked()
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		result, failed, msg, err := fn(context.WithValue(ctx, ctxKey{}, j))
		j.mu.Lock()
		j.data.FinishedAt = time.Now()
		j.data.Result = result
		switch {
		case ctx.Err() != nil:
			j.data.Status = StatusCancelled
			j.data.Error = "cancelled"
		case err != nil:
			j.data.Status = StatusFailed
			j.data.Error = err.Error()
		case failed:
			j.data.Status = StatusFailed
			j.data.Error = msg
		default:
			j.data.Status = StatusCompleted
			j.data.Progress.Percent = 100
			// The estimate is moot now: the last bytes_done is the real size.
			if j.data.Progress.BytesDone > 0 {
				j.data.Progress.BytesTotal = j.data.Progress.BytesDone
			}
		}
		status := j.data.Status
		j.mu.Unlock()
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
		m.logger.Info("background job finished",
			zap.String("job_id", j.data.ID), zap.String("operation", operation), zap.String("status", status))
		if onFinish != nil {
			onFinish(j.snapshot())
		}
	}()
	return j.snapshot(), nil
}

// Get returns one job.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Job{}, false
	}
	return j.snapshot(), true
}

// List returns all remembered jobs, newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	all := make([]*job, 0, len(m.jobs))
	for _, j := range m.jobs {
		all = append(all, j)
	}
	m.mu.Unlock()
	out := make([]Job, 0, len(all))
	for _, j := range all {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(a, b int) bool { return out[a].StartedAt.After(out[b].StartedAt) })
	return out
}

// Cancel asks a running job to stop. It returns false if the job is unknown
// or already finished.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	j.mu.Lock()
	running := j.data.Status == StatusRunning
	j.mu.Unlock()
	if running {
		j.cancel()
	}
	return running
}

// Shutdown cancels every running job and waits for them to stop.
func (m *Manager) Shutdown() {
	m.cancel()
	m.wg.Wait()
}

// evictLocked drops the oldest finished jobs beyond keep. Caller holds m.mu.
func (m *Manager) evictLocked() {
	var finished []*job
	for _, j := range m.jobs {
		j.mu.Lock()
		if j.data.Status != StatusRunning {
			finished = append(finished, j)
		}
		j.mu.Unlock()
	}
	if len(finished) <= m.keep {
		return
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].data.FinishedAt.Before(finished[b].data.FinishedAt) })
	for _, j := range finished[:len(finished)-m.keep] {
		delete(m.jobs, j.data.ID)
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
