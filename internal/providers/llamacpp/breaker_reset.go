package llamacpp

// ResetBreaker force-closes the circuit breakers of the provider's clients
// (the OpenAI-compatible surface and the native root client) so traffic
// resumes immediately after a trip, without a restart. The props client is
// deliberately left alone: it carries no breaker.
func (p *Provider) ResetBreaker() {
	p.compatible.ResetBreaker()
	p.rootClient.ResetBreaker()
}
