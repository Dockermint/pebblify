package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/queue"
	"github.com/Dockermint/Pebblify/internal/daemon/telemetry"
)

// maxRequestBodyBytes caps the size of POST /v1/jobs bodies to prevent
// unbounded memory growth on hostile clients.
const maxRequestBodyBytes = 64 << 10

// handler owns the request-scoped dependencies that power each endpoint.
// It is constructed once per Server and reused across every request.
type handler struct {
	queue      queue.Queue
	logger     *slog.Logger
	collectors *telemetry.Collectors
	version    string
	startedAt  time.Time
}

// newHandler builds a handler with the given dependencies. The version string
// is surfaced verbatim by GET /v1/status; the collectors pointer may be nil
// when telemetry is disabled (the collectors helper methods are nil-safe).
func newHandler(q queue.Queue, logger *slog.Logger, collectors *telemetry.Collectors,
	version string, startedAt time.Time) *handler {
	return &handler{
		queue:      q,
		logger:     logger,
		collectors: collectors,
		version:    version,
		startedAt:  startedAt,
	}
}

// handleSubmitJob processes POST /v1/jobs. It accepts a JSON body, builds a
// queue.Job, and delegates dedup/backpressure to queue.Enqueue. The method
// check is performed by the dispatcher in server.go.
func (h *handler) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	req, err := decodeSubmitJobRequest(w, r)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge,
				errorResponse{Error: "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	canonical, err := queue.Canonicalize(req.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid url: " + err.Error()})
		return
	}

	jobID, err := newJobID()
	if err != nil {
		h.logger.Error("job id generation failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	job := queue.Job{
		ID:          jobID,
		URL:         canonical,
		SubmittedAt: time.Now().UTC(),
	}

	if err := h.queue.Enqueue(job); err != nil {
		h.writeEnqueueError(w, err)
		return
	}

	depth := h.queue.Depth()
	h.collectors.RecordEnqueue(depth)
	writeJSON(w, http.StatusCreated, submitJobResponse{
		JobID:      jobID,
		QueueDepth: depth,
	})
}

// writeEnqueueError maps queue sentinel errors to the appropriate HTTP status.
func (h *handler) writeEnqueueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, queue.ErrDuplicate):
		existingID := ""
		if cur := h.queue.Current(); cur != nil {
			existingID = cur.ID
		}
		writeJSON(w, http.StatusConflict, duplicateResponse{
			Error: "duplicate",
			JobID: existingID,
		})
	case errors.Is(err, queue.ErrQueueFull):
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "queue full"})
	case errors.Is(err, queue.ErrShuttingDown):
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "daemon shutting down"})
	case errors.Is(err, queue.ErrInvalidURL):
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	default:
		h.logger.Error("enqueue failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}

// handleListJobs processes GET /v1/jobs. The method check is performed by
// the dispatcher in server.go.
func (h *handler) handleListJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jobsResponse{
		QueueDepth: h.queue.Depth(),
		Current:    currentView(h.queue.Current()),
	})
}

// handleStatus processes GET /v1/status.
func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Version:       h.version,
		QueueDepth:    h.queue.Depth(),
		Current:       currentView(h.queue.Current()),
		UptimeSeconds: int64(time.Since(h.startedAt).Seconds()),
	})
}

// currentView projects a queue.Job pointer into the HTTP-safe view.
func currentView(j *queue.Job) *currentJobView {
	if j == nil {
		return nil
	}
	return &currentJobView{
		JobID:       j.ID,
		URL:         j.URL,
		SubmittedAt: j.SubmittedAt,
	}
}

// decodeSubmitJobRequest parses and validates the submit job body. Passing
// w into http.MaxBytesReader wires the server-side limit notification so the
// connection is closed cleanly when a hostile client exceeds the cap.
func decodeSubmitJobRequest(w http.ResponseWriter, r *http.Request) (submitJobRequest, error) {
	var req submitJobRequest
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer func() { _ = body.Close() }()

	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return submitJobRequest{}, errors.New("request body must be a JSON object")
		}
		return submitJobRequest{}, err
	}
	if strings.TrimSpace(req.URL) == "" {
		return submitJobRequest{}, errors.New("url is required")
	}
	return req, nil
}

// writeJSON serializes v as JSON and sets the status code and Content-Type.
// Encoding errors are logged via slog.Default because the response writer
// state is already committed by the time an error could surface.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Error("api response encode failed", "error", err)
	}
}

// newJobID produces a 128-bit random identifier encoded as a lowercase hex
// string. The crypto/rand source is used so identifiers are unpredictable to
// clients and safe as idempotency keys.
func newJobID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
