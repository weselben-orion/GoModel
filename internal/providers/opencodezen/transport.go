package opencodezen

import "net/http"

// freeTierHTTPClient returns the HTTP client the free-tier lane uses. With
// the "opencodezen_utls" build tag it is a client whose TLS ClientHello
// matches Bun's (Cloudflare JA4 gate); without the tag it reports that the
// transport is unavailable and the free-tier lane fails with a typed
// config error instead of silently presenting the Go fingerprint.
func freeTierHTTPClient() (*http.Client, error) {
	return bunTLSClient()
}
