package llmclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newBreakerTestServer answers every request with status and JSON body so a
// reset test can trip the breaker through real requests and prove admission
// afterwards.
func newBreakerTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}
