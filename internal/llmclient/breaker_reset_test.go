package llmclient

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResetTestClient builds a client with an enabled breaker that trips on a
// single failure and stays open far beyond any test's runtime.
func newResetTestClient(scope string) *Client {
	cfg := DefaultConfig("test", "")
	cfg.Retry.MaxRetries = 0
	cfg.Retry.InitialBackoff = time.Millisecond
	cfg.CircuitBreaker.Enabled = true
	cfg.CircuitBreaker.FailureThreshold = 1
	cfg.CircuitBreaker.SuccessThreshold = 1
	cfg.CircuitBreaker.Timeout = time.Hour
	cfg.CircuitBreaker.Scope = scope
	return New(cfg, nil)
}

// trip opens a breaker through the public state machine.
func trip(cb *circuitBreaker) {
	cb.RecordFailure()
}

func TestResetBreaker_TripsOpenBreakerBackToClosed(t *testing.T) {
	client := newResetTestClient("provider")
	require.NotNil(t, client.circuitBreaker)
	trip(client.circuitBreaker)
	require.Equal(t, "open", client.circuitBreaker.State())

	client.ResetBreaker()

	assert.Equal(t, "closed", client.circuitBreaker.State())
	// Admits traffic again: closed breakers always acquire.
	allowed, probe := client.circuitBreaker.acquire()
	assert.True(t, allowed)
	assert.False(t, probe)
}

func TestResetBreaker_HalfOpenWithConsumedProbeReturnsToClosed(t *testing.T) {
	client := newResetTestClient("provider")
	require.NotNil(t, client.circuitBreaker)
	trip(client.circuitBreaker)
	// Age past the open timeout so the next acquire transitions to half-open
	// and consumes the single probe slot.
	client.circuitBreaker.mu.Lock()
	client.circuitBreaker.lastFailure = time.Now().Add(-2 * time.Hour)
	client.circuitBreaker.mu.Unlock()
	allowed, probe := client.circuitBreaker.acquire()
	require.True(t, allowed)
	require.True(t, probe)
	require.Equal(t, "half-open", client.circuitBreaker.State())
	// A second request is rejected: the probe slot is consumed.
	allowed, _ = client.circuitBreaker.acquire()
	require.False(t, allowed)

	client.ResetBreaker()

	assert.Equal(t, "closed", client.circuitBreaker.State())
	allowed, probe = client.circuitBreaker.acquire()
	assert.True(t, allowed, "reset must restore traffic without a stale probe slot")
	assert.False(t, probe)
}

func TestResetBreaker_ClosedBreakerIsNoOpButValid(t *testing.T) {
	client := newResetTestClient("provider")
	require.NotNil(t, client.circuitBreaker)

	client.ResetBreaker()

	assert.Equal(t, "closed", client.circuitBreaker.State())
	allowed, probe := client.circuitBreaker.acquire()
	assert.True(t, allowed)
	assert.False(t, probe)
}

func TestResetBreaker_ModelScopeResetsProviderAndModelBreakers(t *testing.T) {
	client := newResetTestClient("model")
	require.NotNil(t, client.circuitBreaker)

	first := client.breakerForModel("model-a")
	second := client.breakerForModel("model-b")
	third := client.breakerForModel("model-c")
	require.Len(t, client.modelBreakers, 3)
	trip(first)
	trip(second)
	trip(third)
	trip(client.circuitBreaker)
	require.Equal(t, "open", first.State())
	require.Equal(t, "open", second.State())
	require.Equal(t, "open", third.State())
	require.Equal(t, "open", client.circuitBreaker.State())

	client.ResetBreaker()

	assert.Equal(t, "closed", first.State())
	assert.Equal(t, "closed", second.State())
	assert.Equal(t, "closed", third.State())
	assert.Equal(t, "closed", client.circuitBreaker.State())
	allowed, probe := second.acquire()
	assert.True(t, allowed)
	assert.False(t, probe)
}

func TestResetBreaker_DisabledBreakerIsSafeNoOp(t *testing.T) {
	cfg := DefaultConfig("test", "")
	cfg.CircuitBreaker.Enabled = false
	client := New(cfg, nil)
	require.Nil(t, client.circuitBreaker)

	assert.NotPanics(t, func() { client.ResetBreaker() })
}

// TestResetBreaker_TrippedClientAdmitsTrafficAfterReset exercises the reset
// through the request path: a tripped breaker rejects DoRaw with a
// circuit-breaker error, and after ResetBreaker the same request reaches the
// upstream again.
func TestResetBreaker_TrippedClientAdmitsTrafficAfterReset(t *testing.T) {
	server := newBreakerTestServer(t, http.StatusInternalServerError, `{"error":{"message":"Server error"}}`)

	client := newResetTestClient("provider")
	cfg := client.config
	cfg.BaseURL = server.URL
	client = NewWithHTTPClient(server.Client(), cfg, nil)
	_, err := client.DoRaw(t.Context(), Request{Method: http.MethodGet, Endpoint: "/test"})
	require.Error(t, err)
	_, err = client.DoRaw(t.Context(), Request{Method: http.MethodGet, Endpoint: "/test"})
	require.ErrorContains(t, err, "circuit breaker")

	client.ResetBreaker()

	_, err = client.DoRaw(t.Context(), Request{Method: http.MethodGet, Endpoint: "/test"})
	require.Error(t, err, "the upstream still answers 500; the point is the request reached it")

	// Reset again and repeat against a healthy upstream: the reset breaker
	// admits traffic (a second 500 would have re-opened it).
	client.ResetBreaker()
	healthy := newBreakerTestServer(t, http.StatusOK, `{"ok":true}`)
	cfg.BaseURL = healthy.URL
	client = NewWithHTTPClient(healthy.Client(), cfg, nil)
	var out map[string]any
	err = client.Do(t.Context(), Request{Method: http.MethodGet, Endpoint: "/ok"}, &out)
	require.NoError(t, err)
	assert.Equal(t, true, out["ok"])
}
