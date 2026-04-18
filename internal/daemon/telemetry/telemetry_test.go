package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// scrapeMetrics gathers from reg via promhttp.HandlerFor and returns the raw text.
func scrapeMetrics(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d", rr.Code)
	}
	return rr.Body.String()
}

// ---- Collectors helper method tests ----

// TestCollectors_RecordEnqueue_IncrementsEnqueuedAndSetsDepth.
func TestCollectors_RecordEnqueue_IncrementsEnqueuedAndSetsDepth(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		JobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "t", Name: "jobs_total", Help: "h",
		}, []string{"status"}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "t", Name: "queue_depth", Help: "h",
		}),
	}
	_ = reg.Register(c.JobsTotal)
	_ = reg.Register(c.QueueDepth)

	c.RecordEnqueue(5)

	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, `status="enqueued"`) {
		t.Errorf("RecordEnqueue: missing enqueued label in metrics: %s", body)
	}
	if !strings.Contains(body, "5") {
		t.Errorf("RecordEnqueue: queue_depth 5 not in metrics: %s", body)
	}
}

// TestCollectors_RecordDequeue_UpdatesQueueDepth.
func TestCollectors_RecordDequeue_UpdatesQueueDepth(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "t", Name: "queue_depth_d", Help: "h",
		}),
	}
	_ = reg.Register(c.QueueDepth)

	c.RecordDequeue(3)
	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, "3") {
		t.Errorf("RecordDequeue: depth 3 not in metrics: %s", body)
	}
}

// TestCollectors_RecordJobStart_SetsActiveToOne.
func TestCollectors_RecordJobStart_SetsActiveToOne(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		Active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "t", Name: "active_s", Help: "h",
		}),
	}
	_ = reg.Register(c.Active)
	c.RecordJobStart()
	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, "1") {
		t.Errorf("RecordJobStart: active not 1 in metrics: %s", body)
	}
}

// TestCollectors_RecordJobEnd_Success_SetsCompletedAndClearsActive.
func TestCollectors_RecordJobEnd_Success_SetsCompletedAndClearsActive(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		JobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "t", Name: "jobs_total_end", Help: "h",
		}, []string{"status"}),
		JobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "t", Name: "job_duration_end", Help: "h", Buckets: prometheus.DefBuckets,
		}),
		Active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "t", Name: "active_end", Help: "h",
		}),
	}
	_ = reg.Register(c.JobsTotal)
	_ = reg.Register(c.JobDuration)
	_ = reg.Register(c.Active)

	c.Active.Set(1)
	c.RecordJobEnd(500*time.Millisecond, true)

	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, `status="completed"`) {
		t.Errorf("RecordJobEnd success: missing completed label in: %s", body)
	}
	// Active must be reset to 0. Unlabeled gauge is exposed as "t_active_end <value>" (no braces).
	if !strings.Contains(body, "t_active_end 0") {
		t.Errorf("RecordJobEnd: active not reset to 0 after job end: %s", body)
	}
}

// TestCollectors_RecordJobEnd_Failure_SetsFailedLabel.
func TestCollectors_RecordJobEnd_Failure_SetsFailedLabel(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		JobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "t", Name: "jobs_total_fail", Help: "h",
		}, []string{"status"}),
		JobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "t", Name: "job_dur_fail", Help: "h", Buckets: prometheus.DefBuckets,
		}),
		Active: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "t", Name: "active_fail", Help: "h",
		}),
	}
	_ = reg.Register(c.JobsTotal)
	_ = reg.Register(c.JobDuration)
	_ = reg.Register(c.Active)

	c.RecordJobEnd(time.Second, false)

	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, `status="failed"`) {
		t.Errorf("RecordJobEnd failure: missing failed label in: %s", body)
	}
}

// TestCollectors_AddBytesDownloaded_Accumulates.
func TestCollectors_AddBytesDownloaded_Accumulates(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		BytesDownloaded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "t", Name: "bytes_dl", Help: "h",
		}),
	}
	_ = reg.Register(c.BytesDownloaded)

	c.AddBytesDownloaded(1024)
	c.AddBytesDownloaded(1024)

	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, "2048") {
		t.Errorf("AddBytesDownloaded: 2048 not in metrics: %s", body)
	}
}

// TestCollectors_AddBytesUploaded_Accumulates.
func TestCollectors_AddBytesUploaded_Accumulates(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		BytesUploaded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "t", Name: "bytes_ul", Help: "h",
		}),
	}
	_ = reg.Register(c.BytesUploaded)

	c.AddBytesUploaded(512)
	c.AddBytesUploaded(512)

	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, "1024") {
		t.Errorf("AddBytesUploaded: 1024 not in metrics: %s", body)
	}
}

// TestCollectors_NilSafe_AllMethodsOnNilPointer must not panic.
func TestCollectors_NilSafe_AllMethodsOnNilPointer(t *testing.T) {
	t.Parallel()
	var c *Collectors
	// None of these must panic.
	c.RecordEnqueue(5)
	c.RecordDequeue(5)
	c.RecordJobStart()
	c.RecordJobEnd(time.Second, true)
	c.RecordJobEnd(time.Second, false)
	c.AddBytesDownloaded(100)
	c.AddBytesUploaded(100)
	c.AddBytesDownloaded(0)  // zero value is skipped
	c.AddBytesUploaded(-1)   // negative is skipped
}

// TestCollectors_AddBytesDownloaded_ZeroAndNegativeSkipped.
func TestCollectors_AddBytesDownloaded_ZeroAndNegativeSkipped(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	c := &Collectors{
		BytesDownloaded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "t", Name: "bytes_dl_zero", Help: "h",
		}),
	}
	_ = reg.Register(c.BytesDownloaded)

	c.AddBytesDownloaded(0)
	c.AddBytesDownloaded(-100)

	body := scrapeMetrics(t, reg)
	// Counter should remain at 0. Unlabeled counter is exposed as "t_bytes_dl_zero <value>" (no braces).
	if !strings.Contains(body, "t_bytes_dl_zero 0") {
		t.Errorf("AddBytesDownloaded zero/negative incremented counter, expected t_bytes_dl_zero 0 in: %s", body)
	}
}

// ---- New constructor ----

// TestNew_Disabled_ReturnsNilNilNil when enable = false.
func TestNew_Disabled_ReturnsNilNilNil(t *testing.T) {
	t.Parallel()
	srv, cols, err := New(config.TelemetrySection{Enable: false}, nil)
	if err != nil {
		t.Fatalf("New(disabled) error = %v", err)
	}
	if srv != nil {
		t.Errorf("New(disabled) srv = %+v, want nil", srv)
	}
	if cols != nil {
		t.Errorf("New(disabled) cols = %+v, want nil", cols)
	}
}

// TestNew_Enabled_ReturnsServerAndCollectors calls New() with telemetry enabled
// and asserts the returned server and collectors are non-nil, then immediately
// stops the server to avoid address-in-use errors in parallel tests.
func TestNew_Enabled_ReturnsServerAndCollectors(t *testing.T) {
	// Not parallel — mutates the global prometheus registry via New().
	cfg := config.TelemetrySection{
		Enable: true,
		Host:   "127.0.0.1",
		Port:   0, // Shutdown is called before Start so no bind occurs.
	}

	srv, cols, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New(enabled) error = %v", err)
	}
	if srv == nil {
		t.Error("New(enabled) srv = nil, want non-nil")
	}
	if cols == nil {
		t.Fatal("New(enabled) cols = nil, want non-nil")
	}

	// Unregister collectors to restore the global registry for other tests.
	t.Cleanup(func() {
		prometheus.Unregister(cols.JobsTotal)
		prometheus.Unregister(cols.JobDuration)
		prometheus.Unregister(cols.QueueDepth)
		prometheus.Unregister(cols.BytesDownloaded)
		prometheus.Unregister(cols.BytesUploaded)
		prometheus.Unregister(cols.Active)
	})

	// Shutdown an unstarted http.Server returns nil immediately.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if stopErr := srv.Stop(stopCtx); stopErr != nil {
		t.Errorf("srv.Stop() error = %v, want nil", stopErr)
	}
}

// TestBuildCollectors_DoubleRegistrationReturnsError.
func TestBuildCollectors_DoubleRegistrationReturnsError(t *testing.T) {
	// Not parallel — mutates the global prometheus registry.
	cols, err := buildCollectors()
	if err != nil {
		t.Fatalf("first buildCollectors() error = %v", err)
	}
	defer func() {
		prometheus.Unregister(cols.JobsTotal)
		prometheus.Unregister(cols.JobDuration)
		prometheus.Unregister(cols.QueueDepth)
		prometheus.Unregister(cols.BytesDownloaded)
		prometheus.Unregister(cols.BytesUploaded)
		prometheus.Unregister(cols.Active)
	}()

	_, err = buildCollectors()
	if err == nil {
		t.Error("second buildCollectors() expected error (double registration), got nil")
	}
}

// TestJobStatusConstants verifies label values.
func TestJobStatusConstants(t *testing.T) {
	t.Parallel()
	if JobStatusEnqueued != "enqueued" {
		t.Errorf("JobStatusEnqueued = %q, want %q", JobStatusEnqueued, "enqueued")
	}
	if JobStatusCompleted != "completed" {
		t.Errorf("JobStatusCompleted = %q, want %q", JobStatusCompleted, "completed")
	}
	if JobStatusFailed != "failed" {
		t.Errorf("JobStatusFailed = %q, want %q", JobStatusFailed, "failed")
	}
}
