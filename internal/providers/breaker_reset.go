package providers

import (
	"errors"
	"fmt"
	"strings"
)

// ErrProviderNotFound reports a provider instance name nothing is registered
// under right now.
var ErrProviderNotFound = errors.New("provider not found")

// ErrProviderBreakerResetUnsupported reports a registered provider whose
// circuit breaker(s) cannot be reset from this package — today: Bedrock, which
// drives the AWS SDK outside the llmclient engine.
var ErrProviderBreakerResetUnsupported = errors.New("provider has no llmclient circuit breaker to reset")

// BreakerResetter force-closes a named provider's circuit breaker(s).
type BreakerResetter interface {
	// ResetCircuitBreaker resets every breaker the named provider owns.
	// Disabled breakers are a successful no-op. It returns
	// ErrProviderNotFound for an unknown name and
	// ErrProviderBreakerResetUnsupported when the provider has no
	// llmclient breaker at all.
	ResetCircuitBreaker(providerName string) error
}

type registryBreakerResetter struct {
	registry *ModelRegistry
}

func NewBreakerResetter(registry *ModelRegistry) BreakerResetter {
	return registryBreakerResetter{registry: registry}
}

func (r registryBreakerResetter) ResetCircuitBreaker(providerName string) error {
	providerName = strings.TrimSpace(providerName)
	provider := r.registry.ProviderByName(providerName)
	if provider == nil {
		return fmt.Errorf("%w: %s", ErrProviderNotFound, providerName)
	}
	// Every llmclient-backed provider implements ResetBreaker by delegating
	// to its client(s); adding a new provider means adding that 3-line
	// method. Bedrock does not — it drives the AWS SDK outside llmclient —
	// and is reported as unsupported instead of a silent success.
	rs, ok := provider.(interface{ ResetBreaker() })
	if !ok {
		return fmt.Errorf("%w: %s", ErrProviderBreakerResetUnsupported, providerName)
	}
	rs.ResetBreaker()
	return nil
}
