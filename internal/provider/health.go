package provider

import (
	"sync"
	"time"
)

// Health tracks per-provider success/failure and implements the circuit
// breaker: after maxConsecutiveFailures within failureWindow the provider
// is skipped for breakDuration (half-open probe after), unless it is the
// only route for the mode.
const (
	maxConsecutiveFailures = 3
	failureWindow          = 5 * time.Minute
	breakDuration          = 2 * time.Minute
)

// providerHealth is the mutable state for one provider.
type providerHealth struct {
	consecutive int
	firstFail   time.Time
	lastFail    time.Time
	breakUntil  time.Time
	lastSuccess time.Time
}

// Health is an in-memory circuit breaker shared by services. The zero
// value is usable. It is safe for concurrent use.
type Health struct {
	mu     sync.Mutex
	states map[string]*providerHealth
	now    func() time.Time
}

// state returns the health record for name, creating it on first use.
func (h *Health) state(name string) *providerHealth {
	if h.states == nil {
		h.states = make(map[string]*providerHealth)
	}
	st, ok := h.states[name]
	if !ok {
		st = &providerHealth{}
		h.states[name] = st
	}
	return st
}

// clock returns the current time, honoring the test hook.
func (h *Health) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// RecordSuccess clears the failure streak for the provider.
func (h *Health) RecordSuccess(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.state(name)
	st.consecutive = 0
	st.firstFail = time.Time{}
	st.lastSuccess = h.clock()
	if h.clock().After(st.breakUntil) {
		st.breakUntil = time.Time{}
	}
}

// RecordFailure registers one failure; after maxConsecutiveFailures
// within failureWindow the provider breaks for breakDuration.
func (h *Health) RecordFailure(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	st := h.state(name)
	if st.consecutive == 0 || now.Sub(st.firstFail) > failureWindow {
		st.consecutive = 0
		st.firstFail = now
	}
	st.consecutive++
	st.lastFail = now
	if st.consecutive >= maxConsecutiveFailures && now.Sub(st.firstFail) <= failureWindow {
		st.breakUntil = now.Add(breakDuration)
	}
}

// Skipped reports whether the provider must be skipped right now. When
// onlyRoute is true the breaker never skips: a lone provider always gets
// its half-open probe. The reason names the break expiry for toasts/logs.
func (h *Health) Skipped(name string, onlyRoute bool) (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.states[name]
	if !ok {
		return false, ""
	}
	now := h.clock()
	if now.After(st.breakUntil) || st.breakUntil.IsZero() {
		return false, ""
	}
	if onlyRoute {
		return false, ""
	}
	return true, "circuit open until " + st.breakUntil.Format(time.Kitchen)
}

// FailedProviders feeds ranking: providers whose last failure is recent
// enough to demote. Callers pass now so tests stay deterministic.
func (h *Health) FailedProviders(now time.Time) map[string]time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]time.Time, len(h.states))
	for name, st := range h.states {
		if st.lastFail.IsZero() {
			continue
		}
		if now.Sub(st.lastFail) < 10*time.Minute {
			out[name] = st.lastFail
		}
	}
	return out
}
