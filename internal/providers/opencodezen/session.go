package opencodezen

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// Header names the OpenCode identity headers the free-tier gate checks.
const (
	sessionHeader = "x-opencode-session"
	requestHeader = "x-opencode-request"
	clientHeader  = "x-opencode-client"
	projectHeader = "x-opencode-project"
)

// Identity values fixed by the gate: the client must look like the
// OpenCode CLI on the global project.
const (
	zenClient  = "cli"
	zenProject = "global"
)

// inferenceUserAgent is the user agent the OpenCode CLI sends on the
// inference lane. The model-listing lane uses Bun/<ver> instead, but the
// chat-completions lane is the one the gate fingerprints.
const inferenceUserAgent = "opencode/latest/2.2.7/cli"

// sessionHeadEnvVar names the provisioned OpenCode session head. The gate
// binds admission to a session id it has seen: an invented id is rejected
// no matter how correct the rest of the request is, while a captured head
// keeps working for hours. Only the twelve hex characters after ses_ are
// checked; the tail can be replaced freely. Capture one real session once
// (a single `opencode run` against a local capture listener) and store the
// head here.
const sessionHeadEnvVar = "OPENCODEZEN_SESSION_HEAD"

// fallbackSessionHead stands in when no session head was provisioned. It
// preserves the ses_ shape so the provider still speaks the protocol; the
// upstream rejects it with FreeUsageLimitError until a real head is
// provisioned, which is exactly how an unprovisioned client fails.
const fallbackSessionHead = "ses_0123456789abcdef0123456789abcdef"

// zenIdentity holds the provisioned session head and mints the per-request
// x-opencode-request id the gate requires fresh (stale or malformed ids are
// rejected with 403). The id format follows the CLI: msg_ + 12 hex time
// chars + 14 base62 chars.
type zenIdentity struct {
	session string
}

// newZenIdentity resolves the session head from envVar, falling back to a
// synthetic head when unset.
func newZenIdentity(envVar string) *zenIdentity {
	head := strings.TrimSpace(os.Getenv(envVar))
	if head == "" {
		head = fallbackSessionHead
	}
	return &zenIdentity{session: head}
}

// requestID mints a fresh x-opencode-request id: msg_ + 12 hex time chars +
// 14 base62 chars.
func (z *zenIdentity) requestID() string {
	return "msg_" + timeHex() + base62(14)
}

// timeHex returns the current time as 12 hex characters (low 48 bits of the
// nanosecond clock, big-endian).
func timeHex() string {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(time.Now().UnixNano()))
	return hex.EncodeToString(raw[2:])
}

// base62 renders n random base62 characters. Freshness, not entropy, is what
// the gate checks; crypto/rand keeps the ids unguessable regardless.
func base62(n int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			// crypto/rand does not fail in practice; a zero digit keeps
			// the id well-formed if it ever does.
			out[i] = alphabet[0]
			continue
		}
		out[i] = alphabet[idx.Int64()]
	}
	return string(out)
}

// requestHeaders returns the per-request identification headers the
// free-tier gate expects: the provisioned session head, a fresh request id,
// the cli client identity, the global project, and the CLI user agent.
func requestHeaders(ident *zenIdentity) func(context.Context) http.Header {
	return func(context.Context) http.Header {
		headers := make(http.Header, 8)
		headers.Set("User-Agent", inferenceUserAgent)
		headers.Set(clientHeader, zenClient)
		headers.Set(projectHeader, zenProject)
		headers.Set(sessionHeader, ident.session)
		headers.Set("x-opencode-session-id", ident.session)
		headers.Set("x-session-id", ident.session)
		headers.Set("x-session-affinity", ident.session)
		headers.Set(requestHeader, ident.requestID())
		return headers
	}
}

// chatRequestHeaders adapts requestHeaders to the ChatRequestHeaders hook
// signature of the OpenAI-compatible provider.
func chatRequestHeaders(fn func(context.Context) http.Header) func(context.Context, *core.ChatRequest) http.Header {
	return func(ctx context.Context, _ *core.ChatRequest) http.Header {
		return fn(ctx)
	}
}

// withDefaultHeaders returns a copy of headers with every entry of defaults
// added that headers does not already carry. Names are compared
// case-insensitively so a non-canonical caller key still counts as present.
func withDefaultHeaders(headers, defaults http.Header) http.Header {
	merged := headers.Clone()
	if merged == nil {
		merged = make(http.Header, len(defaults))
	}
	for name, values := range defaults {
		if hasHeader(merged, name) {
			continue
		}
		merged[http.CanonicalHeaderKey(name)] = values
	}
	return merged
}

// hasHeader reports whether headers carries a non-empty entry for name under
// any casing of the key.
func hasHeader(headers http.Header, name string) bool {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return true
		}
	}
	return false
}
