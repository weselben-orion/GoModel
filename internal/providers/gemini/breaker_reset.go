package gemini

// ResetBreaker force-closes the circuit breakers of every client the provider
// owns (chat, native, models) so traffic resumes immediately after a trip,
// without a restart.
func (p *Provider) ResetBreaker() {
	p.client.ResetBreaker()
	p.nativeClient.ResetBreaker()
	p.modelsClient.ResetBreaker()
}
