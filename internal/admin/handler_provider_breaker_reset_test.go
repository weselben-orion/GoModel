package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// breakerResetMockProvider is a handlerMockProvider plus a ResetBreaker that
// force-closes one real llmclient circuit breaker, so the endpoint test can
// prove the reset flipped an actual breaker instead of only reporting success.
type breakerResetMockProvider struct {
	handlerMockProvider
	client *llmclient.Client
}

func (p *breakerResetMockProvider) ResetBreaker() { p.client.ResetBreaker() }

// newTrippedBreakerProvider registers a provider named name whose breaker is
// open after a real failed upstream call, and returns its client.
func newTrippedBreakerProvider(t *testing.T, registry *providers.ModelRegistry, name string) *llmclient.Client {
	t.Helper()
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	cfg := llmclient.DefaultConfig(name, server.URL)
	cfg.Retry.MaxRetries = 0
	cfg.CircuitBreaker.FailureThreshold = 1
	cfg.CircuitBreaker.SuccessThreshold = 1
	cfg.CircuitBreaker.Timeout = time.Hour
	client := llmclient.New(cfg, func(*http.Request) {})

	registry.RegisterProviderWithNameAndType(
		&breakerResetMockProvider{client: client}, name, "openai")

	_, err := client.DoRaw(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/v1/models"})
	require.Error(t, err)
	_, err = client.DoRaw(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/v1/models"})
	require.ErrorContains(t, err, "circuit breaker", "precondition: breaker is open")
	return client
}

// TestBreakerResetEndpoint_ClosesOpenBreaker asserts POST
// /admin/providers/:name/breaker/reset answers 200 with the documented JSON
// shape and the provider's next request reaches the upstream instead of the
// breaker's 503 short circuit.
func TestBreakerResetEndpoint_ClosesOpenBreaker(t *testing.T) {
	registry := providers.NewModelRegistry()
	client := newTrippedBreakerProvider(t, registry, "flaky")

	h := NewHandler(nil, registry)
	c, rec := echotest.Request(t, http.MethodPost, "/admin/providers/flaky/breaker/reset", nil,
		echotest.WithPathValue("name", "flaky"), echotest.WithPath("/admin/providers/:name/breaker/reset"))
	require.NoError(t, h.ResetProviderBreaker(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Provider      string `json:"provider"`
		BreakersReset int    `json:"breakers_reset"`
		ResetAt       string `json:"reset_at"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "flaky", body.Provider)
	assert.GreaterOrEqual(t, body.BreakersReset, 1)
	parsed, err := time.Parse(time.RFC3339, body.ResetAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), parsed, 5*time.Second)

	// The real proof: traffic is admitted again — the upstream answers with
	// its 500 rather than the breaker's 503 short circuit.
	_, err = client.DoRaw(context.Background(), llmclient.Request{Method: http.MethodGet, Endpoint: "/v1/models"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "circuit breaker", "breaker must be closed after reset")
}

// TestBreakerResetEndpoint_UnknownProvider asserts 404 with a JSON error body
// for a provider name nothing is registered under.
func TestBreakerResetEndpoint_UnknownProvider(t *testing.T) {
	h := NewHandler(nil, providers.NewModelRegistry())
	c, rec := echotest.Request(t, http.MethodPost, "/admin/providers/ghost/breaker/reset", nil,
		echotest.WithPathValue("name", "ghost"), echotest.WithPath("/admin/providers/:name/breaker/reset"))
	require.NoError(t, h.ResetProviderBreaker(c))
	require.Equal(t, http.StatusNotFound, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotEmpty(t, body["error"])
}

// TestBreakerResetEndpoint_ProviderWithoutResetSupport asserts a provider
// type without reset support (bedrock drives the AWS SDK outside llmclient)
// answers with an explicit error, not a silent success: nothing was reset.
func TestBreakerResetEndpoint_ProviderWithoutResetSupport(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&handlerMockProvider{}, "legacy", "openai")

	h := NewHandler(nil, registry)
	c, rec := echotest.Request(t, http.MethodPost, "/admin/providers/legacy/breaker/reset", nil,
		echotest.WithPathValue("name", "legacy"), echotest.WithPath("/admin/providers/:name/breaker/reset"))
	require.NoError(t, h.ResetProviderBreaker(c))
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "has no llmclient circuit breaker to reset")
}

// TestBreakerResetEndpoint_NilRegistry asserts the handler reports the feature
// as unavailable when it was constructed without a provider registry.
func TestBreakerResetEndpoint_NilRegistry(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Request(t, http.MethodPost, "/admin/providers/flaky/breaker/reset", nil,
		echotest.WithPathValue("name", "flaky"), echotest.WithPath("/admin/providers/:name/breaker/reset"))
	require.NoError(t, h.ResetProviderBreaker(c))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestBreakerResetEndpoint_MissingProviderName asserts an empty :name never
// resets an arbitrary provider: the route parameter is required.
func TestBreakerResetEndpoint_MissingProviderName(t *testing.T) {
	h := NewHandler(nil, providers.NewModelRegistry())
	c, rec := echotest.Request(t, http.MethodPost, "/admin/providers//breaker/reset", nil,
		echotest.WithPathValue("name", "   "), echotest.WithPath("/admin/providers/:name/breaker/reset"))
	require.NoError(t, h.ResetProviderBreaker(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "provider name is required")
}
