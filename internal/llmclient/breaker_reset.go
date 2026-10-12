package llmclient

// Reset force-closes the breaker: state closed, counters zeroed, and the
// half-open probe slot restored so no stale probe blocks traffic. Safe to call
// in any state.
func (cb *circuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = circuitClosed
	cb.failures = 0
	cb.successes = 0
	cb.halfOpenAllowed = true
}

// ResetBreaker force-closes the provider-level breaker and every model-scoped
// breaker so traffic resumes immediately after a trip, without a restart.
// Safe when the breaker is disabled.
//
// Lock order: modelBreakersMu → per-breaker mutex, always in that order.
// Never call back into the modelBreakers map from breaker code. A model
// breaker created concurrently with this reset is missed by the iteration —
// that is fine: it can only exist because breakerForModel just handed it out
// fresh (state closed), so it needs no reset.
func (c *Client) ResetBreaker() {
	if c.circuitBreaker != nil {
		c.circuitBreaker.Reset()
	}
	c.modelBreakersMu.Lock()
	defer c.modelBreakersMu.Unlock()
	for _, entry := range c.modelBreakers {
		entry.breaker.Reset()
	}
}
