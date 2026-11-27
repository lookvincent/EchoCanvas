package gateway

import (
	"sync"
	"time"
)

// State represents circuit breaker states.
type State string

const (
	StateClosed   State = "CLOSED"
	StateOpen     State = "OPEN"
	StateHalfOpen State = "HALF_OPEN"
)

// CircuitBreaker implements a sliding-window (1 minute) failure detector with cooldown and half-open probing.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            State
	threshold        int
	window           time.Duration
	cooldown         time.Duration
	failures         []time.Time
	openedAt         time.Time
	halfOpenInFlight bool
	now              func() time.Time
}

// NewCircuitBreaker constructs a CircuitBreaker with 1-minute failure window.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	return NewCircuitBreakerWithClock(threshold, cooldown, time.Now)
}

// NewCircuitBreakerWithClock constructs a CircuitBreaker with an injectable clock for deterministic tests.
func NewCircuitBreakerWithClock(threshold int, cooldown time.Duration, nowFn func() time.Time) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	return &CircuitBreaker{
		state:     StateClosed,
		threshold: threshold,
		window:    time.Minute,
		cooldown:  cooldown,
		now:       nowFn,
	}
}

// State returns the current effective breaker state.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == StateOpen && cb.now().Sub(cb.openedAt) >= cb.cooldown {
		return StateHalfOpen
	}
	return cb.state
}

// Allow reports whether a request may proceed through this breaker.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.now()
	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(cb.openedAt) < cb.cooldown {
			return false
		}
		cb.state = StateHalfOpen
		cb.halfOpenInFlight = true
		return true
	case StateHalfOpen:
		if cb.halfOpenInFlight {
			return false
		}
		cb.halfOpenInFlight = true
		return true
	default:
		return false
	}
}

// RecordSuccess resets failures and returns the breaker to CLOSED.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failures = nil
	cb.halfOpenInFlight = false
}

// RecordFailure records an upstream failure inside the sliding window and opens if threshold is reached.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.now()
	if cb.state == StateHalfOpen {
		cb.state = StateOpen
		cb.openedAt = now
		cb.halfOpenInFlight = false
		return
	}

	cutoff := now.Add(-cb.window)
	pruned := cb.failures[:0]
	for _, ts := range cb.failures {
		if ts.After(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	pruned = append(pruned, now)
	cb.failures = pruned

	if len(cb.failures) >= cb.threshold {
		cb.state = StateOpen
		cb.openedAt = now
	}
}
