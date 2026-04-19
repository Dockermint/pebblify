// Package telemetry exposes daemon-scoped Prometheus collectors and the
// HTTP listener that serves /metrics.
//
// The collectors are registered against the default process-wide registry so
// the existing internal/prom counters (pebblify_keys_processed_total, ...)
// continue to appear alongside the daemon-specific series. The HTTP listener
// is independent from the API and health listeners per the daemon spec; each
// has its own port and timeout budget.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// Metric label values used with Collectors.JobsTotal.
const (
	// JobStatusEnqueued increments when a job is accepted into the queue.
	JobStatusEnqueued = "enqueued"
	// JobStatusCompleted increments when a job finishes successfully.
	JobStatusCompleted = "completed"
	// JobStatusFailed increments when a job ends in error.
	JobStatusFailed = "failed"
)

// Server listener timeouts per the daemon spec telemetry row.
const (
	telemetryReadHeaderTimeout = 5 * time.Second
	telemetryReadTimeout       = 5 * time.Second
	telemetryWriteTimeout      = 10 * time.Second
	telemetryIdleTimeout       = 30 * time.Second
)

// metricsNamespace is the Prometheus namespace shared by every daemon series.
const metricsNamespace = "pebblify_daemon"

// Collectors groups every daemon-scoped Prometheus metric. A nil Collectors
// value is safe: its methods are no-ops so wiring code may pass a nil into
// the runner when telemetry is disabled.
type Collectors struct {
	// JobsTotal counts jobs transitioning through queue/complete/fail states.
	JobsTotal *prometheus.CounterVec
	// JobDuration tracks end-to-end job processing latency.
	JobDuration prometheus.Histogram
	// QueueDepth mirrors queue.Depth as a gauge.
	QueueDepth prometheus.Gauge
	// BytesDownloaded aggregates archive bytes fetched from snapshot URLs.
	BytesDownloaded prometheus.Counter
	// BytesUploaded aggregates archive bytes pushed to save targets.
	BytesUploaded prometheus.Counter
	// Active is 1 while a job is running, 0 otherwise.
	Active prometheus.Gauge
}

// Server is the contract implemented by the Prometheus listener.
type Server interface {
	// Start binds the listener and serves until ctx is cancelled or Stop is
	// invoked. A nil return means clean shutdown.
	Start(ctx context.Context) error
	// Stop performs a graceful shutdown bounded by ctx.
	Stop(ctx context.Context) error
}

// ErrDisabled indicates New was called with an empty or disabled TelemetrySection.
var ErrDisabled = errors.New("telemetry disabled")

// telemetryServer binds its own http.Server and exposes promhttp.Handler.
type telemetryServer struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// New registers Collectors against the default Prometheus registry and returns
// an HTTP server that exposes /metrics on cfg.Host:cfg.Port.
//
// Callers receive both the server and the collectors; if cfg.Enable is false,
// New returns (nil, nil, nil) — telemetry is entirely optional. If registration
// fails (for example because collectors were already registered by a previous
// New call in the same process), the error is returned verbatim.
func New(cfg config.TelemetrySection, logger *slog.Logger) (Server, *Collectors, error) {
	if !cfg.Enable {
		return nil, nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	collectors, err := buildCollectors()
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry register: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	srv := &telemetryServer{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: telemetryReadHeaderTimeout,
			ReadTimeout:       telemetryReadTimeout,
			WriteTimeout:      telemetryWriteTimeout,
			IdleTimeout:       telemetryIdleTimeout,
		},
		logger: logger,
	}
	return srv, collectors, nil
}

// buildCollectors constructs every daemon-scoped collector and registers it
// against the default Prometheus registry. On any registration error the
// partially-registered collectors are unregistered to keep the global
// registry clean on retry.
func buildCollectors() (*Collectors, error) {
	c := &Collectors{
		JobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "jobs_total",
			Help:      "Total number of daemon jobs by status (enqueued|completed|failed).",
		}, []string{"status"}),
		JobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Name:      "job_duration_seconds",
			Help:      "End-to-end duration of daemon jobs in seconds.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 12),
		}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Name:      "queue_depth",
			Help:      "Number of jobs currently waiting in the daemon FIFO queue.",
		}),
		BytesDownloaded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "bytes_downloaded_total",
			Help:      "Total bytes downloaded from snapshot URLs across all jobs.",
		}),
		BytesUploaded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Name:      "bytes_uploaded_total",
			Help:      "Total bytes uploaded to save targets across all jobs.",
		}),
		Active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Name:      "active",
			Help:      "1 while a daemon job is running, 0 otherwise.",
		}),
	}

	toRegister := []prometheus.Collector{
		c.JobsTotal, c.JobDuration, c.QueueDepth,
		c.BytesDownloaded, c.BytesUploaded, c.Active,
	}

	registered := make([]prometheus.Collector, 0, len(toRegister))
	for _, m := range toRegister {
		if err := prometheus.Register(m); err != nil {
			for _, r := range registered {
				prometheus.Unregister(r)
			}
			return nil, err
		}
		registered = append(registered, m)
	}
	return c, nil
}

// Start implements Server. It blocks until the context is cancelled or the
// underlying listener returns. http.ErrServerClosed from graceful shutdown is
// suppressed; any other error is propagated.
func (s *telemetryServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("telemetry listen %s: %w", s.httpServer.Addr, err)
	}
	s.logger.Info("telemetry listener started", "addr", s.httpServer.Addr)

	serveErr := make(chan error, 1)
	go func() {
		err := s.httpServer.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("telemetry shutdown: %w", err)
		}
		<-serveErr
		return nil
	case err := <-serveErr:
		return err
	}
}

// Stop implements Server.
func (s *telemetryServer) Stop(ctx context.Context) error {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("telemetry stop: %w", err)
	}
	return nil
}

// RecordEnqueue increments the enqueued counter and updates queue_depth.
// Safe to call on a nil Collectors — the call is a no-op.
func (c *Collectors) RecordEnqueue(depth int) {
	if c == nil {
		return
	}
	c.JobsTotal.WithLabelValues(JobStatusEnqueued).Inc()
	c.QueueDepth.Set(float64(depth))
}

// RecordDequeue updates queue_depth after a dequeue.
func (c *Collectors) RecordDequeue(depth int) {
	if c == nil {
		return
	}
	c.QueueDepth.Set(float64(depth))
}

// RecordJobStart flips active to 1.
func (c *Collectors) RecordJobStart() {
	if c == nil {
		return
	}
	c.Active.Set(1)
}

// RecordJobEnd observes the job duration, updates the success/failure counter,
// and clears the active gauge.
func (c *Collectors) RecordJobEnd(duration time.Duration, success bool) {
	if c == nil {
		return
	}
	c.JobDuration.Observe(duration.Seconds())
	status := JobStatusCompleted
	if !success {
		status = JobStatusFailed
	}
	c.JobsTotal.WithLabelValues(status).Inc()
	c.Active.Set(0)
}

// AddBytesDownloaded records archive bytes fetched from a snapshot URL.
func (c *Collectors) AddBytesDownloaded(n int64) {
	if c == nil || n <= 0 {
		return
	}
	c.BytesDownloaded.Add(float64(n))
}

// AddBytesUploaded records archive bytes pushed to a save target.
func (c *Collectors) AddBytesUploaded(n int64) {
	if c == nil || n <= 0 {
		return
	}
	c.BytesUploaded.Add(float64(n))
}
