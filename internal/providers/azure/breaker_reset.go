package azure

import "github.com/enterpilot/gomodel/internal/providers"

var _ providers.BreakerReset = (*Provider)(nil)

// ResetBreaker force-closes the circuit breakers of every client the provider
// owns (deployments surface, resource surface, OpenAI resource surface) so
// traffic resumes immediately after a trip, without a restart.
func (p *Provider) ResetBreaker() {
	p.CompatibleProvider.ResetBreaker()
	p.resourceProvider.ResetBreaker()
	p.openAIResourceProvider.ResetBreaker()
}
