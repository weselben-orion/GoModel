package gemini

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

// TestProvider_ResetBreakerWalksAllClients proves the reset reaches all three
// clients (chat, native, models): each breaker is tripped, then the reset
// closes them and traffic is admitted again. The chat surface (p.client) is
// only reachable with the OpenAI-compatible API mode, while native mode routes
// ChatCompletion and ListModels fallbacks through nativeClient — so two
// provider instances are built and each breaker is tripped where it alone
// serves the call.
func TestProvider_ResetBreakerWalksAllClients(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusInternalServerError, `{"error":{"message":"Server error"}}`)

	// OpenAI-compatible mode: ChatCompletion rides p.client.
	t.Setenv(useNativeAPIEnvVar, "false")
	compatOpts := providertest.Options(llmclient.Hooks{})
	compatOpts.HTTPClient = server.Client()
	compatOpts.Resilience.CircuitBreaker.FailureThreshold = 1
	compatOpts.Resilience.CircuitBreaker.SuccessThreshold = 1
	compatOpts.Resilience.CircuitBreaker.Timeout = time.Hour
	compat := New(providers.ProviderConfig{APIKey: "test", BaseURL: server.URL + "/openai", APIMode: "openai-compatible"}, compatOpts).(*Provider)

	_, err := compat.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-2.5-pro"})
	require.Error(t, err)
	_, err = compat.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-2.5-pro"})
	require.ErrorContains(t, err, "circuit breaker", "precondition: chat-surface breaker is open")
	compat.ResetBreaker()
	_, err = compat.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-2.5-pro"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "circuit breaker", "chat-surface breaker must be closed after reset")

	// Native mode: ChatCompletion rides nativeClient; ListModels rides
	// modelsClient (non-nil, so the nativeClient fallback never applies).
	t.Setenv(useNativeAPIEnvVar, "false")
	nativeOpts := providertest.Options(llmclient.Hooks{})
	nativeOpts.HTTPClient = server.Client()
	nativeOpts.Resilience.CircuitBreaker.FailureThreshold = 1
	nativeOpts.Resilience.CircuitBreaker.SuccessThreshold = 1
	nativeOpts.Resilience.CircuitBreaker.Timeout = time.Hour
	native := New(providers.ProviderConfig{APIKey: "test", BaseURL: server.URL, APIMode: "gemini_native"}, nativeOpts).(*Provider)

	_, err = native.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-2.5-pro"})
	require.Error(t, err)
	_, err = native.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-2.5-pro"})
	require.ErrorContains(t, err, "circuit breaker", "precondition: native-surface breaker is open")

	_, err = native.ListModels(context.Background())
	require.Error(t, err)
	_, err = native.ListModels(context.Background())
	require.ErrorContains(t, err, "circuit breaker", "precondition: models-surface breaker is open")

	native.ResetBreaker()

	// Every surface is admitted again: the upstream answers (its 500) instead
	// of the gateway short-circuiting.
	_, err = native.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-2.5-pro"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "circuit breaker", "native-surface breaker must be closed after reset")
	_, err = native.ListModels(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "circuit breaker", "models-surface breaker must be closed after reset")
}

// TestProvider_ResetBreakerWithDisabledBreakerIsSafeNoOp covers a provider
// built with the circuit breaker disabled: the reset must not panic.
func TestProvider_ResetBreakerWithDisabledBreakerIsSafeNoOp(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"data":[]}`)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	opts.Resilience.CircuitBreaker.Enabled = false
	p := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, opts).(*Provider)

	assert.NotPanics(t, func() { p.ResetBreaker() })
}
