package health

import (
	"sync"
	"time"
)

type ProbeState struct {
	mu            sync.RWMutex
	started       bool
	ready         bool
	lastPing      time.Time
	livenessGrace time.Duration
}

func NewProbeState(livenessGrace time.Duration) *ProbeState {
	return &ProbeState{
		livenessGrace: livenessGrace,
		lastPing:      time.Now(),
	}
}

func (p *ProbeState) SetStarted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = true
}

func (p *ProbeState) SetReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready = true
}

func (p *ProbeState) SetNotReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ready = false
}

func (p *ProbeState) Ping() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastPing = time.Now()
}

func (p *ProbeState) IsStarted() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.started
}

func (p *ProbeState) IsReady() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready
}

func (p *ProbeState) IsAlive() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return time.Since(p.lastPing) < p.livenessGrace
}
