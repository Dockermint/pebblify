// Package api implements the daemon's HTTP control plane.
//
// Endpoints:
//   - POST /v1/jobs    — enqueue a snapshot conversion job
//   - GET  /v1/jobs    — queue depth + current job
//   - GET  /v1/status  — daemon state + build version
//
// The listener is independent from the health and telemetry listeners per
// the daemon spec.
package api

import "time"

// submitJobRequest is the JSON body accepted by POST /v1/jobs.
type submitJobRequest struct {
	// URL is the snapshot archive location to download and convert.
	URL string `json:"url"`
}

// submitJobResponse is the 201 Created body.
type submitJobResponse struct {
	// JobID is the opaque identifier assigned to the enqueued job.
	JobID string `json:"job_id"`
	// QueueDepth is the queue depth observed immediately after the enqueue.
	QueueDepth int `json:"queue_depth"`
}

// duplicateResponse is the 409 Conflict body returned when the URL is
// already queued or running.
type duplicateResponse struct {
	// Error is a stable machine-readable error code ("duplicate").
	Error string `json:"error"`
	// JobID is the existing job's identifier when available.
	JobID string `json:"job_id,omitempty"`
}

// errorResponse is the generic 4xx/5xx body with a human-readable message.
type errorResponse struct {
	// Error is the reason the request was rejected.
	Error string `json:"error"`
}

// currentJobView mirrors the subset of queue.Job safe to surface over HTTP.
type currentJobView struct {
	// JobID is the opaque identifier.
	JobID string `json:"job_id"`
	// URL is the snapshot URL under processing.
	URL string `json:"url"`
	// SubmittedAt is the enqueue timestamp in RFC3339 format.
	SubmittedAt time.Time `json:"submitted_at"`
}

// jobsResponse is returned by GET /v1/jobs.
type jobsResponse struct {
	// QueueDepth is the number of jobs waiting in the FIFO.
	QueueDepth int `json:"queue_depth"`
	// Current describes the job in progress, or nil when idle.
	Current *currentJobView `json:"current"`
}

// statusResponse is returned by GET /v1/status.
type statusResponse struct {
	// Version is the daemon build version.
	Version string `json:"version"`
	// QueueDepth is the number of jobs waiting in the FIFO.
	QueueDepth int `json:"queue_depth"`
	// Current describes the job in progress, or nil when idle.
	Current *currentJobView `json:"current"`
	// UptimeSeconds is the number of seconds since the API server started.
	UptimeSeconds int64 `json:"uptime_seconds"`
}
