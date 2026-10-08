package providers

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/enterpilot/gomodel/internal/llmclient"
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
	if !resetClientBreakers(provider) {
		return fmt.Errorf("%w: %s", ErrProviderBreakerResetUnsupported, providerName)
	}
	return nil
}

// resetClientBreakers walks the provider's breaker(s) and resets them. It
// first checks for any type that implements a ResetBreaker method (so
// OpenAI-compatible adapters can opt into walking their own multi-client
// state), then falls back to one reflect-driven field scan per type —
// adding a new provider needs no new code here.
//
// The return value reports whether anything was reset, not how many
// breakers: the number of llmclient.Client fields is a per-type
// implementation detail, and an interface-path provider may carry one
// provider-level breaker plus any number of model-scoped breakers.
func resetClientBreakers(provider any) bool {
	if r, ok := provider.(interface{ ResetBreaker() }); ok {
		r.ResetBreaker()
		return true
	}

	// Fall back to a single reflect pass: every llmclient-built provider
	// exposes its client(s) as exported *llmclient.Client fields (one for
	// plain adapters, more for multi-client providers like gemini/azure).
	resetAny := false
	v := reflect.ValueOf(provider)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if !f.CanInterface() {
			continue
		}
		client, ok := f.Interface().(*llmclient.Client)
		if !ok || client == nil {
			continue
		}
		client.ResetBreaker()
		resetAny = true
	}
	return resetAny
}
