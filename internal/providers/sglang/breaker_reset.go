package sglang

// ResetBreaker force-closes the circuit breakers of the provider's clients
// (the OpenAI-compatible surface and the native root client) so traffic
// resumes immediately after a trip, without a restart.
func (p *Provider) ResetBreaker() {
	p.compatible.ResetBreaker()
	p.rootClient.ResetBreaker()
}
