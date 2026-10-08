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
	// ResetCircuitBreaker returns 1 when the provider instance was reset and
	// 0 when it has no llmclient breaker at all. Disabled breakers are
	// counted as reset (the call is a safe no-op for them).
	ResetCircuitBreaker(providerName string) (reset int, err error)
}

type registryBreakerResetter struct {
	registry *ModelRegistry
}

func NewBreakerResetter(registry *ModelRegistry) BreakerResetter {
	return registryBreakerResetter{registry: registry}
}

func (r registryBreakerResetter) ResetCircuitBreaker(providerName string) (int, error) {
	providerName = strings.TrimSpace(providerName)
	provider := r.registry.ProviderByName(providerName)
	if provider == nil {
		return 0, fmt.Errorf("%w: %s", ErrProviderNotFound, providerName)
	}
	n := resetClientBreakers(provider)
	if n == 0 {
		return 0, fmt.Errorf("%w: %s", ErrProviderBreakerResetUnsupported, providerName)
	}
	return n, nil
}

// resetClientBreakers walks the provider's breaker(s) and resets them. It
// first checks for any type that implements a ResetBreaker method (so
// OpenAI-compatible adapters can opt into walking their own multi-client
// state), then falls back to one reflect-driven field scan per type —
// adding a new provider needs no new code here.
//
// The return value counts provider instances reset, not individual
// breakers: an interface-path provider reports 1 even though its client
// carries one provider-level breaker plus every model-scoped breaker, so
// the reflect path reports 1 for the whole provider as well (the number of
// llmclient.Client fields is a per-type implementation detail). Both paths
// report 0 when nothing was reset.
func resetClientBreakers(provider any) int {
	if r, ok := provider.(interface{ ResetBreaker() }); ok {
		r.ResetBreaker()
		return 1
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
		return 0
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
	if resetAny {
		return 1
	}
	return 0
}
