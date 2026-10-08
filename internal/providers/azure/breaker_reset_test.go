package azure

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingTestProvider builds a provider whose breakers trip on the first
// failing request, pointed at a 500-upstream.
func failingTestProvider(t *testing.T) *Provider {
	t.Helper()
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":{"message":"Server error"}}`)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	opts.Resilience.CircuitBreaker.FailureThreshold = 1
	opts.Resilience.CircuitBreaker.SuccessThreshold = 1
	opts.Resilience.CircuitBreaker.Timeout = time.Hour
	p := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, opts).(*Provider)
	return p
}

// TestProvider_ResetBreakerWalksAllClients proves the reset reaches every one
// of the provider's three CompatibleProvider clients: each surface's breaker
// is tripped through a real failing request first.
func TestProvider_ResetBreakerWalksAllClients(t *testing.T) {
	p := failingTestProvider(t)

	// The three surfaces share one provider but use three separate clients, so
	// each has its own breaker. ListModels routes through the resource
	// provider; its first failure opens that breaker (FailureThreshold 1), so
	// the second call must be short-circuited.
	_, err := p.ListModels(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "circuit breaker", "precondition: first ListModels reaches the upstream")
	err = p.resourceProvider.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/openai/models"}, nil)
	require.ErrorContains(t, err, "circuit breaker", "precondition: resource surface is tripped")
	err = p.openAIResourceProvider.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/openai/models"}, nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "circuit breaker", "precondition: openai-resource surface reached the upstream")

	p.ResetBreaker()

	// After the reset the previously short-circuited surface is admitted: the
	// breaker is closed, so the upstream answers (with its 500) itself.
	err = p.resourceProvider.Do(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/openai/models"}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "circuit breaker", "resource-surface breaker must be closed after reset")
}

// TestProvider_ResetBreakerAdmitsTrafficAfterTrip drives the full cycle on
// the chat surface: failing requests open the breaker, short-circuiting
// starts, the reset closes it, and a request succeeds against a recovered
// upstream.
func TestProvider_ResetBreakerAdmitsTrafficAfterTrip(t *testing.T) {
	failing, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":{"message":"Server error"}}`)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = failing.Client()
	opts.Resilience.CircuitBreaker.FailureThreshold = 1
	opts.Resilience.CircuitBreaker.SuccessThreshold = 1
	opts.Resilience.CircuitBreaker.Timeout = time.Hour
	p := New(providers.ProviderConfig{APIKey: "key", BaseURL: failing.URL}, opts).(*Provider)

	_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "model-a"})
	require.Error(t, err)
	_, err = p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "model-a"})
	require.ErrorContains(t, err, "circuit breaker")

	healthy, _ := providertest.JSONServer(t, http.StatusOK, `{"id":"resp","model":"model-a","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)
	p.SetBaseURL(healthy.URL)
	p.ResetBreaker()

	resp, err := p.ChatCompletion(context.Background(), &core.ChatRequest{Model: "model-a"})
	require.NoError(t, err)
	assert.Equal(t, "resp", resp.ID)
}
