//go:build !opencodezen_utls

package opencodezen

import (
	"errors"
	"net/http"
)

// errNoUTLS reports that the free-tier lane needs the Bun TLS fingerprint
// but the binary was built without the "opencodezen_utls" build tag.
var errNoUTLS = errors.New("opencodezen: the free-tier lane needs the Bun TLS ClientHello; rebuild with -tags opencodezen_utls")

// bunTLSClient is unavailable without the "opencodezen_utls" build tag; the
// free-tier lane therefore fails with a typed config error on first use.
func bunTLSClient() (*http.Client, error) {
	return nil, errNoUTLS
}
