// Package worker runs agent-role stages: it manages LLM session concurrency and
// circuit-breaking, dispatches Kafka stage requests to role handlers, and
// checkpoints results.
package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

// ErrCircuitOpen is returned when the breaker is open and rejecting calls.
var ErrCircuitOpen = errors.New("worker: circuit breaker open")

// SessionConfig configures the session manager.
type SessionConfig struct {
	MaxConcurrent    int
	FailureThreshold int
	Cooldown         time.Duration
}

// SessionManager caps concurrent LLM calls and trips a circuit breaker on
// repeated upstream failures — treating LLM sessions as failable compute.
type SessionManager struct {
	mu      sync.Mutex
	backend llm.Backend
	sem     chan struct{}
	now     func() time.Time

	threshold   int
	cooldown    time.Duration
	consecutive int
	openUntil   time.Time // when > now, breaker is open
	halfOpen    bool
}

// NewSessionManager builds a manager with the given config and defaults.
func NewSessionManager(backend llm.Backend, cfg SessionConfig) *SessionManager {
	return newSessionManagerWithClock(backend, cfg, time.Now)
}

func newSessionManagerWithClock(backend llm.Backend, cfg SessionConfig, now func() time.Time) *SessionManager {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 30 * time.Second
	}
	return &SessionManager{
		backend:   backend,
		sem:       make(chan struct{}, cfg.MaxConcurrent),
		now:       now,
		threshold: cfg.FailureThreshold,
		cooldown:  cfg.Cooldown,
	}
}

// swapBackend replaces the backend (test helper for breaker recovery).
func (m *SessionManager) swapBackend(b llm.Backend) {
	m.mu.Lock()
	m.backend = b
	m.mu.Unlock()
}

// Complete runs one completion through the breaker + concurrency limiter.
func (m *SessionManager) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if !m.allow() {
		return llm.Response{}, ErrCircuitOpen
	}
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		m.mu.Lock()
		m.halfOpen = false // we may have taken the half-open trial slot but never ran it
		m.mu.Unlock()
		return llm.Response{}, ctx.Err()
	}

	m.mu.Lock()
	backend := m.backend
	m.mu.Unlock()

	resp, err := backend.Complete(ctx, req)
	m.record(err == nil)
	if err != nil {
		return resp, fmt.Errorf("worker: session complete: %w", err)
	}
	return resp, nil
}

// allow reports whether a call may proceed, transitioning open→half-open when
// the cooldown has elapsed. Exactly one half-open trial is admitted at a
// time; concurrent callers are rejected until that trial's record() resolves
// the breaker state.
func (m *SessionManager) allow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.openUntil.IsZero() {
		return true // closed
	}
	if m.halfOpen {
		return false // a half-open trial is already in flight — reject others
	}
	if m.now().Before(m.openUntil) {
		return false // still open, cooldown not elapsed
	}
	// Cooldown elapsed and no trial in flight → admit exactly one trial.
	m.halfOpen = true
	return true
}

// record updates breaker state after a call.
func (m *SessionManager) record(success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if success {
		m.consecutive = 0
		m.openUntil = time.Time{}
		m.halfOpen = false
		return
	}
	m.consecutive++
	if m.halfOpen || m.consecutive >= m.threshold {
		m.openUntil = m.now().Add(m.cooldown)
		m.halfOpen = false
	}
}
