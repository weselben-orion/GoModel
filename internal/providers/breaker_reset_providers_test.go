package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/providers/groq"
)

// tripBreakerResilience returns a ResilienceConfig with the breaker tripping
// on the first failure and staying open for an hour, so a test can flip it
// with one request and only the reset under test closes it again.
func tripBreakerResilience() config.ResilienceConfig {
	r := config.ResilienceConfig{
		Retry:          config.DefaultRetryConfig(),
		CircuitBreaker: config.DefaultCircuitBreakerConfig(),
	}
	r.Retry.MaxRetries = 0
	r.CircuitBreaker.FailureThreshold = 1
	r.CircuitBreaker.SuccessThreshold = 1
	r.CircuitBreaker.Timeout = time.Hour
	return r
}

// failServer answers every request with a 500, enough to trip a breaker at
// threshold 1.
func failServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// chatOnce issues one chat completion, the minimum traffic that exercises a
// provider's breaker.
func chatOnce(p core.Provider, model string) error {
	_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    model,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	return err
}

// TestBreakerResetter_RealProvider_Anthropic pins the production shape the
// first review pass missed: anthropic keeps its *llmclient.Client in an
// unexported field, so only an explicit ResetBreaker delegation reaches its
// breaker. Trips the real breaker, resets through the public interface, then
// asserts traffic is admitted again.
func TestBreakerResetter_RealProvider_Anthropic(t *testing.T) {
	server := failServer(t)

	registry := providers.NewModelRegistry()
	provider := anthropic.New(providers.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}, providers.ProviderOptions{Resilience: tripBreakerResilience()})
	registry.RegisterProviderWithNameAndType(provider, "anthropic", "anthropic")

	require.Error(t, chatOnce(provider, "claude-test"))
	require.ErrorContains(t, chatOnce(provider, "claude-test"), "circuit breaker",
		"precondition: breaker is open")

	require.NoError(t, providers.NewBreakerResetter(registry).ResetCircuitBreaker("anthropic"))

	err := chatOnce(provider, "claude-test")
	require.Error(t, err, "upstream still answers 500; point is the request reached it")
	assert.NotContains(t, err.Error(), "circuit breaker", "breaker must be closed after reset")
}

// TestBreakerResetter_RealProvider_Groq pins the other production shape:
// providers that wrap *openai.CompatibleProvider in an unexported compat
// field. Same trip-then-reset flow through the real adapter.
func TestBreakerResetter_RealProvider_Groq(t *testing.T) {
	server := failServer(t)

	registry := providers.NewModelRegistry()
	provider := groq.New(providers.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}, providers.ProviderOptions{Resilience: tripBreakerResilience()})
	registry.RegisterProviderWithNameAndType(provider, "groq", "groq")

	require.Error(t, chatOnce(provider, "llama-test"))
	require.ErrorContains(t, chatOnce(provider, "llama-test"), "circuit breaker",
		"precondition: breaker is open")

	require.NoError(t, providers.NewBreakerResetter(registry).ResetCircuitBreaker("groq"))

	err := chatOnce(provider, "llama-test")
	require.Error(t, err, "upstream still answers 500; point is the request reached it")
	assert.NotContains(t, err.Error(), "circuit breaker", "breaker must be closed after reset")
}
