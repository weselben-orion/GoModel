package xai

// ResetBreaker force-closes the provider's circuit breaker(s) so traffic
// resumes immediately after a trip, without a restart.
func (p *Provider) ResetBreaker() {
	p.compat.ResetBreaker()
}
