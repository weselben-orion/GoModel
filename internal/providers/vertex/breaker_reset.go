package vertex

// ResetBreaker force-closes the circuit breakers of the provider's clients
// (the embedded Gemini adapter and the native prediction client) so traffic
// resumes immediately after a trip, without a restart.
func (p *Provider) ResetBreaker() {
	p.gemini.ResetBreaker()
	p.nativeClient.ResetBreaker()
}
