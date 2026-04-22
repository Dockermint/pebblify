package prom

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Process-wide Prometheus collectors exported by the prom package.
//
// Every collector lives in the "pebblify" namespace and is registered with
// the default Prometheus registry during package init, so importers only
// need to mutate them directly.
var (
	// KeysProcessed counts the total number of keys written to PebbleDB
	// across all runs served by this process.
	KeysProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "keys_processed_total",
		Help:      "Total number of keys processed by pebblify.",
	})

	// BytesRead counts the total number of bytes read from source
	// LevelDB instances across all runs served by this process.
	BytesRead = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "bytes_read_total",
		Help:      "Total number of bytes read by pebblify.",
	})

	// BytesWritten counts the total number of bytes written to target
	// PebbleDB instances across all runs served by this process.
	BytesWritten = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "bytes_written_total",
		Help:      "Total number of bytes written by pebblify.",
	})

	// Databases gauges the number of sub-databases in each lifecycle
	// state (pending, in_progress, done, failed).
	Databases = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "pebblify",
		Name:      "databases_total",
		Help:      "Current number of databases managed by pebblify, labeled by status.",
	}, []string{"status"})

	// KeysPerSecond gauges the current throughput in keys per second.
	KeysPerSecond = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pebblify",
		Name:      "keys_per_second",
		Help:      "Current rate of keys processed per second by pebblify.",
	})

	// BytesPerSecond gauges the current throughput in bytes per second.
	BytesPerSecond = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pebblify",
		Name:      "bytes_per_second",
		Help:      "Current rate of bytes processed per second by pebblify.",
	})

	// BatchCommits counts the total number of PebbleDB batch commits
	// performed across all runs served by this process.
	BatchCommits = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "batch_commits_total",
		Help:      "Total number of batch commits performed by pebblify.",
	})

	// Checkpoints counts the total number of on-disk state checkpoints
	// taken across all runs served by this process.
	Checkpoints = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "checkpoints_total",
		Help:      "Total number of checkpoints created by pebblify.",
	})
)

func init() {
	prometheus.MustRegister(
		KeysProcessed,
		BytesRead,
		BytesWritten,
		Databases,
		KeysPerSecond,
		BytesPerSecond,
		BatchCommits,
		Checkpoints,
	)
}

// Server is the opt-in HTTP exporter that serves the Prometheus metrics
// exposed by this package on /metrics.
type Server struct {
	httpServer *http.Server
}

// NewServer returns a Server ready to expose /metrics on the given port.
// The server is not started until ListenAndServe is called.
func NewServer(port int) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	return &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       5 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       30 * time.Second,
		},
	}
}

// ListenAndServe binds the configured port and blocks serving HTTP
// requests until the server is shut down.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully stops the server, waiting for in-flight requests
// up to the deadline carried by ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
