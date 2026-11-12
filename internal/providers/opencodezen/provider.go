// Package opencodezen provides the OpenCode Zen free-tier provider for the
// LLM gateway. It is a thin wrapper over the shared OpenAI-compatible
// adapter (openai.CompatibleProvider) that presents the client identity the
// free-tier gate expects: a Bun-shaped TLS ClientHello, OpenCode identity
// headers, stream:true, the 13-tool array, and a captured gate-prompt block
// in the system message.
//
// The uTLS Bun-ClientHello transport is opt-in: it only links when the
// binary is built with the "opencodezen_utls" tag. Without the tag the
// provider still constructs, but the free-tier lane returns a clear typed
// config error on the first chat request.
package opencodezen

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

// defaultBaseURL is the OpenCode Zen free-tier inference lane
// (/chat/completions, OpenAI-shaped SSE). The model-listing lane is the
// operator's override or /zen/v1 (e.g. https://opencode.ai/zen/v1).
// Override with OPENCODEZEN_BASE_URL.
const defaultBaseURL = "https://opencode.ai/inference/openai/v1"

// Registration provides factory registration for the OpenCode Zen free-tier
// provider. The "opencodezen" type derives OPENCODEZEN_API_KEY,
// OPENCODEZEN_BASE_URL, and OPENCODEZEN_MODELS by convention.
var Registration = providers.Registration{
	Type: "opencodezen",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for OpenCode Zen's free
// tier. It embeds *openai.ChatCompatible and inherits the whole
// chat-centric surface (chat, models listing, passthrough, Responses
// translated via chat completions); it only overrides what the free-tier
// gate demands:
//
//   - the transport speaks the Bun TLS ClientHello (uTLS, build-tagged);
//   - AdaptChatRequest forces stream:true, pads the request to the 13-tool
//     array, and prepends the gate-prompt block;
//   - ChatRequestHeaders mints a fresh x-opencode-request id per request
//     and carries the provisioned session head, the cli client identity,
//     and the project hash;
//   - ChatCompletion folds the SSE answer into a buffered completion for
//     non-streaming callers, since the upstream ignores stream:false.
type Provider struct {
	*openai.ChatCompatible
	gate    *gate
	ident   *zenIdentity
	headers func(context.Context) http.Header
	// utlsActive reports whether the uTLS Bun transport was compiled in and
	// selected.
	utlsActive bool
	// gated reports whether this instance talks to the real OpenCode Zen
	// free tier: a gate prompt is provisioned and the base URL is an
	// opencode.ai host. A gated instance without the uTLS transport fails
	// every chat request with a typed config error.
	gated bool
}

var _ core.Provider = (*Provider)(nil)

// New creates a new OpenCode Zen free-tier provider. It reads the gate-prompt
// file and session head from their env vars and fails loudly (an unusable
// provider) when the gate contract cannot be satisfied, so a mis-provisioned
// deployment fails at boot instead of silently 429ing every request.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	loadedGate, err := loadGate(gateFileEnvVar)
	if err != nil {
		return newConfigErrorProvider(err)
	}
	ident := newZenIdentity(sessionHeadEnvVar)
	headers := requestHeaders(ident)

	var httpClient *http.Client
	utlsActive := false
	if c, err := freeTierHTTPClient(); err == nil && isOpenCodeBaseURL(cfg.BaseURL) {
		// The uTLS transport is selected only when it was compiled in AND
		// the target is a real OpenCode host. Operators can point the
		// provider at a mock or mirror and keep a plain transport.
		httpClient = c
		utlsActive = true
	}
	gated := loadedGate.block != "" && isOpenCodeBaseURL(cfg.BaseURL)

	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	chat := openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName:       "opencodezen",
		BaseURL:            baseURL,
		HTTPClient:         httpClient,
		AdaptChatRequest:   adaptChatRequest(loadedGate),
		ChatRequestHeaders: chatRequestHeaders(headers),
	})
	return &Provider{
		ChatCompatible: chat,
		gate:           loadedGate,
		ident:          ident,
		headers:        headers,
		utlsActive:     utlsActive,
		gated:          gated,
	}
}

// ChatCompletion folds the upstream SSE answer into a buffered completion
// for non-streaming callers: the free tier always answers SSE and would
// fail to decode as a JSON body otherwise.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.requireFreeTier("chat completions"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	adapted := adaptGateRequest(p.gate, req)
	stream, err := p.StreamChatCompletion(ctx, adapted)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	resp, err := foldChatSSE(stream, req.Model)
	if err != nil {
		return nil, err
	}
	core.EnsureModel(&resp.Model, req.Model)
	return resp, nil
}

// Responses dispatches through this provider's ChatCompletion so
// /v1/responses picks up the gate adaptation and SSE folding.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, p.ProviderName())
}

// StreamResponses dispatches through this provider's streaming
// ChatCompletion so /v1/responses picks up the gate adaptation.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, p.ProviderName())
}

// Passthrough forwards an opaque request with the identity headers filled
// in wherever the caller did not send its own. Passthrough is opaque by
// contract, so a caller-provided x-opencode-session, x-opencode-request, or
// User-Agent always wins over the gateway's value.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if err := p.requireFreeTier("passthrough"); err != nil {
		return nil, err
	}
	if req == nil || p.headers == nil {
		return p.ChatCompatible.Passthrough(ctx, req)
	}
	merged := *req
	merged.Headers = withDefaultHeaders(req.Headers, p.headers(ctx))
	return p.ChatCompatible.Passthrough(ctx, &merged)
}

// Embeddings returns an error because OpenCode Zen does not expose an
// embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("opencodezen does not support embeddings", nil)
}

// requireFreeTier returns a typed config error when the uTLS Bun transport
// was not compiled in (build without the "opencodezen_utls" tag) while this
// instance is gated (a gate prompt is provisioned against the real
// opencode.ai host). Unprovisioned or mock/mirror instances work on any
// build.
func (p *Provider) requireFreeTier(operation string) error {
	if p.utlsActive || !p.gated {
		return nil
	}
	return core.NewInvalidRequestError(
		"opencodezen "+operation+" requires the opencodezen_utls build tag (the free-tier lane needs the Bun TLS fingerprint); rebuild with -tags opencodezen_utls", nil)
}

// isOpenCodeBaseURL reports whether baseURL points at opencode.ai (or is
// empty, meaning the default). A non-OpenCode override disables the uTLS
// transport and the gate so operators can use mocks and mirrors.
func isOpenCodeBaseURL(baseURL string) bool {
	return baseURL == "" || strings.HasPrefix(baseURL, "https://opencode.ai")
}
