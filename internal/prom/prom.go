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

var (
	KeysProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "keys_processed_total",
	})

	BytesRead = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "bytes_read_total",
	})

	BytesWritten = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "bytes_written_total",
	})

	Databases = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "pebblify",
		Name:      "databases_total",
	}, []string{"status"})

	KeysPerSecond = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pebblify",
		Name:      "keys_per_second",
	})

	BytesPerSecond = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pebblify",
		Name:      "bytes_per_second",
	})

	BatchCommits = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "batch_commits_total",
	})

	Checkpoints = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pebblify",
		Name:      "checkpoints_total",
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

type Server struct {
	httpServer *http.Server
}

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

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	return s.httpServer.Serve(ln)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
