package store

import (
	"fmt"
	"sync"
	"time"

	"govdoc/internal/docmodel"
)

// Job is one long-running analysis, streamed to the browser over SSE.
//
// # Why a job rather than a long POST
//
// Analysis takes tens of seconds on a real document, and the user needs to
// see which stage is running. A plain POST would either block with no
// feedback or time out behind a proxy. The job splits the work from the
// observation: POST starts it and returns an id, and the SSE endpoint
// replays the stage history to whichever client connects — so a page reload
// mid-analysis still shows the progress instead of a blank screen.
type Job struct {
	ID      string
	DocID   string
	mu      sync.Mutex
	stages  []StageUpdate
	done    bool
	err     error
	result  *docmodel.Plan
	report  *docmodel.Report
	waiters map[chan StageUpdate]struct{}
	// doneCh is closed when the job settles, so waiters wake without polling.
	doneCh   chan struct{}
	created  time.Time
	finished time.Time
}

// StageUpdate is one progress notification. It mirrors api.StageEvent but
// lives here so the store does not depend on the HTTP layer.
type StageUpdate struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
	// Kind distinguishes an ordinary stage frame from a partial ("partial").
	// It is not serialized: the SSE writer chooses the event name from it.
	Kind string `json:"-"`
	// Error is set on the terminal update when the job failed.
	Error string `json:"error,omitempty"`
	// Report is attached to the terminal update.
	Report *docmodel.Report `json:"report,omitempty"`
	// Plan and Edits carry a partial snapshot while the model is still
	// writing. They are provisional: the arrays may be incomplete and the
	// plan has not been verified.
	Plan  *docmodel.Plan  `json:"plan,omitempty"`
	Edits []docmodel.Edit `json:"edits,omitempty"`
	// Reset clears provisional rows at the start of a new model attempt.
	Reset bool `json:"reset,omitempty"`
}

// NewJob creates a job in the running state.
func (s *Store) NewJob(id, docID string) *Job {
	j := &Job{
		ID:      id,
		DocID:   docID,
		waiters: map[chan StageUpdate]struct{}{},
		doneCh:  make(chan struct{}),
		created: time.Now(),
	}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	return j
}

// Job returns a stored job.
func (s *Store) Job(id string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("%w: 分析任务 %s", ErrNotFound, id)
	}
	return j, nil
}

// Publish records a stage update and fans it out to live subscribers.
func (j *Job) Publish(u StageUpdate) {
	j.mu.Lock()
	if j.done {
		j.mu.Unlock()
		return
	}
	// A partial frame is a full snapshot, not a delta. Keeping every one in
	// history would make a reload replay hundreds of ever-larger prefixes;
	// only the newest one is useful to a late subscriber.
	if u.Kind == "partial" {
		kept := j.stages[:0]
		for _, prev := range j.stages {
			if prev.Kind != "partial" {
				kept = append(kept, prev)
			}
		}
		j.stages = kept
	}
	j.stages = append(j.stages, u)
	waiters := make([]chan StageUpdate, 0, len(j.waiters))
	for ch := range j.waiters {
		waiters = append(waiters, ch)
	}
	j.mu.Unlock()

	for _, ch := range waiters {
		// Non-blocking send: a subscriber whose buffer is full is stalled,
		// and blocking here would stall the analysis for everyone. It can
		// recover the missed frames from History on its next read.
		select {
		case ch <- u:
		default:
		}
	}
}

// Finish settles the job. It may be called once; later calls are no-ops.
//
// The terminal update is delivered to live subscribers on a best-effort
// basis, exactly like a mid-stream stage: a subscriber whose buffer is full
// drops it rather than blocking the analysis. That is intentional, and it is
// why doneCh exists — a subscriber must treat "the channel closed or went
// quiet" as a reason to re-read Result, not as a reason to keep waiting. See
// DoneChan.
func (j *Job) Finish(plan *docmodel.Plan, report *docmodel.Report, err error) {
	j.mu.Lock()
	if j.done {
		j.mu.Unlock()
		return
	}
	j.done = true
	j.result, j.report, j.err = plan, report, err
	j.finished = time.Now()
	last := StageUpdate{Done: true, Report: report}
	if err != nil {
		last.Error = err.Error()
		last.Message = "分析失败"
	} else {
		last.Message = "分析完成"
		last.Name = "complete"
	}
	j.stages = append(j.stages, last)
	waiters := make([]chan StageUpdate, 0, len(j.waiters))
	for ch := range j.waiters {
		waiters = append(waiters, ch)
	}
	close(j.doneCh)
	j.mu.Unlock()

	for _, ch := range waiters {
		select {
		case ch <- last:
		default:
		}
	}
}

// DoneChan returns a channel closed when the job settles.
//
// A subscriber must watch this in addition to its update channel. Relying on
// the terminal update arriving through the update channel is a latent hang:
// the send is non-blocking, so a subscriber that is momentarily behind (or
// one that connected just as the job finished) would never observe the
// terminal frame and would wait forever. The authoritative terminal state is
// always available from Result.
func (j *Job) DoneChan() <-chan struct{} { return j.doneCh }

// Subscribe returns the history so far plus a channel of subsequent updates.
// The caller MUST call Unsubscribe when finished, or the channel leaks.
//
// History-then-subscribe (rather than subscribe-only) is what makes a late
// or reloaded client see the stages it missed.
func (j *Job) Subscribe() ([]StageUpdate, chan StageUpdate, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	history := make([]StageUpdate, len(j.stages))
	copy(history, j.stages)
	if j.done {
		// Already settled: the history ends with the terminal update, so no
		// further events can arrive.
		return history, nil, true
	}
	ch := make(chan StageUpdate, 32)
	j.waiters[ch] = struct{}{}
	return history, ch, false
}

// Unsubscribe removes a subscriber channel.
func (j *Job) Unsubscribe(ch chan StageUpdate) {
	if ch == nil {
		return
	}
	j.mu.Lock()
	delete(j.waiters, ch)
	j.mu.Unlock()
}

// Done reports whether the job has settled.
func (j *Job) Done() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

// Result returns the settled outcome. The second value reports whether the
// job has finished at all.
func (j *Job) Result() (*docmodel.Plan, *docmodel.Report, error, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.report, j.err, j.done
}

// Wait blocks until the job settles or the timeout elapses.
func (j *Job) Wait(d time.Duration) bool {
	select {
	case <-j.doneCh:
		return true
	case <-time.After(d):
		return false
	}
}

// CleanupJobs drops settled jobs older than ttl. Called by the server's
// periodic sweep so a long-lived process does not accumulate job records.
func (s *Store) CleanupJobs(ttl time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-ttl)
	n := 0
	for id, j := range s.jobs {
		j.mu.Lock()
		settled := j.done && !j.finished.IsZero() && j.finished.Before(cutoff)
		waiters := len(j.waiters)
		j.mu.Unlock()
		if settled && waiters == 0 {
			delete(s.jobs, id)
			n++
		}
	}
	return n
}
