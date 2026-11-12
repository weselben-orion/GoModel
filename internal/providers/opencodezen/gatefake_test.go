package opencodezen

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// newGateFake starts a mock of the Zen free-tier gate. It asserts every
// condition the real gate enforces (identity headers, stream:true, the 13
// gate tools, the gate text in the system message), replying
// FreeTierError-style 403s when one is missing, and answers well-formed
// OpenAI-shaped SSE otherwise. A regression in the adapter fails here, not
// against the live quota.
func newGateFake(t *testing.T, gate string) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/chat/completions": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			reject := func(reason string) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"type":"FreeTierError","message":"` + reason + `"}}`))
			}
			if r.Header.Get(clientHeader) != "cli" {
				reject("missing x-opencode-client")
				return
			}
			if !strings.HasPrefix(r.Header.Get(sessionHeader), "ses_") {
				reject("missing x-opencode-session")
				return
			}
			if r.Header.Get(requestHeader) == "" {
				reject("missing x-opencode-request")
				return
			}
			var body struct {
				Stream   bool              `json:"stream"`
				Messages []json.RawMessage `json:"messages"`
				Tools    []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				reject("bad body")
				return
			}
			if !body.Stream {
				reject("stream must be true")
				return
			}
			if len(body.Tools) < len(gateTools) {
				reject("missing gate tools")
				return
			}
			if len(body.Messages) == 0 || !strings.Contains(string(body.Messages[0]), gate) {
				reject("missing gate prompt")
				return
			}
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-fake\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from the gate fake\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		},
	})
}
