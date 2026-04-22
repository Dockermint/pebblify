package health

import (
	"sync"
	"time"
)

// ProbeState is the concurrent-safe state machine backing the Kubernetes
// style startup, readiness, and liveness probes.
type ProbeState struct {
	mu            sync.RWMutex
	started       bool
	ready         bool
	lastPing      time.Time
	livenessGrace time.Duration
}

// NewProbeState returns a ProbeState with the given livenessGrace window.
// The liveness clock is primed to the current time so a freshly created
// process is reported alive until livenessGrace elapses without a Ping.
func NewProbeState(livenessGrace time.Duration) *ProbeState {
	return &ProbeState{
		livenessGrace: livenessGrace,
		lastPing:      time.Now(),
	}
}

// SetStarted marks the startup probe as successful.
func (p *ProbeState) SetStarted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = true
}

// SetReady marks the readiness probe as successful.
func (p *ProbeState) SetReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready = true
}

// SetNotReady marks the readiness probe as failing.
func (p *ProbeState) SetNotReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready = false
}

// Ping refreshes the liveness timestamp so IsAlive continues to report
// true for another livenessGrace window.
func (p *ProbeState) Ping() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastPing = time.Now()
}

// IsStarted reports whether SetStarted has been called.
func (p *ProbeState) IsStarted() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.started
}

// IsReady reports whether the readiness probe is currently succeeding.
func (p *ProbeState) IsReady() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready
}

// IsAlive reports whether the last Ping occurred within the configured
// livenessGrace window.
func (p *ProbeState) IsAlive() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return time.Since(p.lastPing) < p.livenessGrace
}

// PingTicker drives periodic liveness pings against a ProbeState.
type PingTicker struct {
	stop chan struct{}
}

// NewPingTicker starts a goroutine that calls state.Ping every interval.
// The ticker runs until Stop is invoked.
func NewPingTicker(state *ProbeState, interval time.Duration) *PingTicker {
	pt := &PingTicker{stop: make(chan struct{})}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				state.Ping()
			case <-pt.stop:
				return
			}
		}
	}()

	return pt
}

// Stop halts the background ping goroutine started by NewPingTicker.
func (pt *PingTicker) Stop() {
	close(pt.stop)
}
