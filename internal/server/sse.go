package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"govdoc/internal/api"
	"govdoc/internal/store"
)

// sseHeartbeat keeps the connection alive through proxies that close an idle
// stream. An analysis can sit in one stage for minutes while the model works,
// and without a periodic byte the browser's EventSource would reconnect and
// replay the history, making the progress bar jump backwards.
const sseHeartbeat = 20 * time.Second

// handleJobEvents streams analysis progress as Server-Sent Events.
//
// The stream is replay-then-follow: the job's recorded stages are sent first,
// then live updates. That is what makes a page reload mid-analysis resume the
// progress display instead of showing nothing until the next stage happens to
// fire.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.Job(r.PathValue("jobID"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, api.CodeInternal,
			"当前服务器不支持流式响应。")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// Defeat proxy buffering, which would otherwise hold the whole stream
	// until the response ended — precisely the behaviour SSE exists to avoid.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	history, ch, done := job.Subscribe()
	if ch != nil {
		defer job.Unsubscribe(ch)
	}

	// Replay. The recorded history includes the terminal update, so it must be
	// translated into the same `done` frame the live path emits. Emitting it
	// as an ordinary `stage` frame (which is what the raw history record is)
	// would mean a client that follows the documented contract — wait for
	// `event: done` — hangs forever after a page reload, even though the job
	// finished long ago.
	for _, u := range history {
		if u.Done {
			writeSSEDone(w, job)
			flusher.Flush()
			return
		}
		if !writeSSE(w, "stage", u) {
			return
		}
	}
	if done {
		// Settled with no terminal frame recorded: still honour the contract
		// rather than closing silently after a run of stage frames.
		writeSSEDone(w, job)
		flusher.Flush()
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	ctx := r.Context()
	doneCh := job.DoneChan()
	for {
		select {
		case <-ctx.Done():
			// Client went away (tab closed, navigation, refresh). Returning
			// here is what releases the subscriber slot.
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			if u.Done {
				writeSSEDone(w, job)
				flusher.Flush()
				return
			}
			if !writeSSE(w, "stage", u) {
				return
			}
			flusher.Flush()
		case <-doneCh:
			// The authoritative terminal signal. The terminal chunk may have
			// been dropped by the non-blocking fan-out above (a momentarily
			// full buffer), so completion is detected here and the result is
			// read from the job rather than from the channel. Without this,
			// a stalled subscriber would hang forever with no `done` frame.
			writeSSEDone(w, job)
			flusher.Flush()
			return
		case <-heartbeat.C:
			// A comment frame: ignored by EventSource, but it keeps the
			// connection from being closed as idle.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE emits one named event frame. It reports false when the client has
// gone, which the caller uses to stop writing.
func writeSSE(w http.ResponseWriter, event string, payload any) bool {
	data, err := json.Marshal(payload)
	if err != nil {
		// A payload this package built must marshal; if it does not, the
		// stream cannot continue meaningfully.
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return false
	}
	return true
}

// writeSSEDone emits the terminal frame carrying the plan and its verdict.
func writeSSEDone(w http.ResponseWriter, job *store.Job) {
	plan, report, err, _ := job.Result()
	ev := api.DoneEvent{Plan: plan, Report: report}
	if err != nil {
		ev.Error = err.Error()
	}
	writeSSE(w, "done", ev)
}
