//go:build opencodezen_utls

package opencodezen

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatCompatibleContract exercises the shared chat-compatible contract
// against the gated lane. The contract test drives the provider through the
// default (gated) shape, so it needs the uTLS transport compiled in and runs
// only under the opencodezen_utls build tag.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "opencodezen",
		DefaultBaseURL: "https://opencode.ai/inference/openai/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		Embeddings: false,
	})
	providertest.AssertNoNativeSurfaces(t, New(providers.ProviderConfig{APIKey: "k", BaseURL: "http://127.0.0.1:1"}, providertest.Options(llmclient.Hooks{})))
}

// TestBunTLSClientHandshakes verifies the uTLS transport completes a real TLS
// handshake against a local server and serves a request over it, so the
// Bun-shaped ClientHello path is exercised offline.
func TestBunTLSClientHandshakes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer server.Close()

	client, err := bunTLSClient()
	require.NoError(t, err)
	require.NotNil(t, client)
	// The uTLS dialer derives its tls.Config from the dial address and
	// ignores the transport's TLSClientConfig, so trust the test server's
	// root CA through the shared system pool instead.
	pool, err := x509.SystemCertPool()
	require.NoError(t, err)
	for _, cert := range server.TLS.Certificates {
		for _, raw := range cert.Certificate {
			der, err := x509.ParseCertificate(raw)
			require.NoError(t, err)
			pool.AddCert(der)
		}
	}
	original := utlsConfigRootCAs
	utlsConfigRootCAs = pool
	defer func() { utlsConfigRootCAs = original }()

	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(body))
}

// TestBunClientHelloShape checks the offline facts of the Bun-shaped
// ClientHello spec: 17 cipher suites, no GREASE, http/1.1 as the only ALPN
// protocol, and the X25519MLKEM768 + X25519 key shares.
func TestBunClientHelloShape(t *testing.T) {
	spec := bunClientHello()
	require.NotNil(t, spec)
	assert.Len(t, spec.CipherSuites, 17)

	var alpn []string
	var keyShares []utls.CurveID
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.ALPNExtension:
			alpn = e.AlpnProtocols
		case *utls.KeyShareExtension:
			for _, ks := range e.KeyShares {
				keyShares = append(keyShares, ks.Group)
			}
		}
	}
	assert.Equal(t, []string{"http/1.1"}, alpn, "Bun offers http/1.1 only")
	assert.Equal(t, []utls.CurveID{utls.X25519MLKEM768, utls.X25519}, keyShares)
}
