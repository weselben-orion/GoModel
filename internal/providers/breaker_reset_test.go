package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// resetTrackingProvider adapts a plain provider into one implementing
// ResetBreaker, counting resets.
type resetTrackingProvider struct {
	core.Provider
	resets int
}

func (p *resetTrackingProvider) ResetBreaker() { p.resets++ }

// multiClientProvider owns N llmclient.Client fields so the resetter can
// prove it walks every breaker of a multi-client provider, not just one.
type multiClientProvider struct {
	core.Provider
	A, B, C *llmclient.Client
}

func newClientForTest(t *testing.T, name string) *llmclient.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	t.Cleanup(server.Close)
	cfg := llmclient.DefaultConfig(name, server.URL)
	cfg.Retry.MaxRetries = 0
	cfg.CircuitBreaker.FailureThreshold = 1
	cfg.CircuitBreaker.SuccessThreshold = 1
	cfg.CircuitBreaker.Timeout = time.Hour
	return llmclient.New(cfg, func(*http.Request) {})
}

func TestBreakerResetter_ResetsRegisteredProvider(t *testing.T) {
	registry := NewModelRegistry()
	provider := &resetTrackingProvider{}
	registry.RegisterProviderWithNameAndType(provider, "openai-main", "openai")

	count, err := NewBreakerResetter(registry).ResetCircuitBreaker("openai-main")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, 1, provider.resets)
}

func TestBreakerResetter_TrimsProviderName(t *testing.T) {
	registry := NewModelRegistry()
	provider := &resetTrackingProvider{}
	registry.RegisterProviderWithNameAndType(provider, "openai-main", "openai")

	count, err := NewBreakerResetter(registry).ResetCircuitBreaker("  openai-main  ")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestBreakerResetter_UnknownProvider(t *testing.T) {
	registry := NewModelRegistry()
	_, err := NewBreakerResetter(registry).ResetCircuitBreaker("missing")
	require.ErrorIs(t, err, ErrProviderNotFound)
}

// TestBreakerResetter_WalksEveryClientField proves a provider with several
// *llmclient.Client fields (gemini/azure-style) gets every one of its
// breakers reset by a single call.
func TestBreakerResetter_WalksEveryClientField(t *testing.T) {
	registry := NewModelRegistry()
	provider := &multiClientProvider{
		A: newClientForTest(t, "a"),
		B: newClientForTest(t, "b"),
		C: newClientForTest(t, "c"),
	}
	registry.RegisterProviderWithNameAndType(provider, "multi", "test")

	// Precondition: every client has been tripped.
	for _, c := range []*llmclient.Client{provider.A, provider.B, provider.C} {
		_, err := c.DoRaw(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/x"})
		require.Error(t, err)
		_, err = c.DoRaw(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/x"})
		require.ErrorContains(t, err, "circuit breaker")
	}

	count, err := NewBreakerResetter(registry).ResetCircuitBreaker("multi")
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	// Postcondition: every breaker is closed.
	for _, c := range []*llmclient.Client{provider.A, provider.B, provider.C} {
		_, err := c.DoRaw(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/x"})
		require.Error(t, err, "upstream still answers 500; point is the request reached it")
		assert.NotContains(t, err.Error(), "circuit breaker")
	}
}

func TestBreakerResetter_ProviderWithoutBreakerResetSupport(t *testing.T) {
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&noResetNoClientProvider{}, "legacy", "test")

	_, err := NewBreakerResetter(registry).ResetCircuitBreaker("legacy")
	require.ErrorIs(t, err, ErrProviderBreakerResetUnsupported)
}

// TestResetClientBreakers_DefensiveShapes proves the reflect fallback's
// guard clauses: a nil client field is skipped, and a provider that neither
// implements ResetBreaker nor exposes clients resets nothing. Unexported
// client fields are skipped by the same CanInterface guard the nil check
// rides on.
func TestResetClientBreakers_DefensiveShapes(t *testing.T) {
	// Nil exported client field: seen, but skipped — reset count 0.
	assert.Equal(t, 0, resetClientBreakers(&multiClientProvider{}))
	// Value (non-pointer) provider: reflect dereference reaches the struct.
	withClient := &multiClientProvider{A: newClientForTest(t, "x")}
	assert.Equal(t, 1, resetClientBreakers(*withClient))
	// Non-struct provider value: the reflect fallback bails out at 0.
	assert.Equal(t, 0, resetClientBreakers(42))
	// Unexported client field: the CanInterface guard skips it.
	assert.Equal(t, 0, resetClientBreakers(&unexportedClientProvider{
		hidden: newClientForTest(t, "y"),
	}))
}

// unexportedClientProvider models a provider whose llmclient client is not
// reachable through reflection (unexported field), so the resetter must not
// panic and must report nothing reset.
type unexportedClientProvider struct {
	core.Provider
	hidden *llmclient.Client
}

// noResetNoClientProvider is a provider with neither a ResetBreaker method
// nor any *llmclient.Client fields — exactly bedrock's shape, modeled with
// the minimum surface to prove the resetter refuses it.
type noResetNoClientProvider struct{ core.Provider }
