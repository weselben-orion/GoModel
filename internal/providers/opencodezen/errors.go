package opencodezen

import (
	"context"
	"fmt"
	"io"

	"github.com/enterpilot/gomodel/internal/core"
)

// configErrorProvider is a core.Provider that fails every call with a typed
// invalid-request error. New returns it when the gate contract cannot be
// satisfied at boot (for example a truncated gate file), so the failure is
// loud at startup instead of silently 429ing every request.
type configErrorProvider struct {
	err error
}

var _ core.Provider = (*configErrorProvider)(nil)

func newConfigErrorProvider(err error) *configErrorProvider {
	return &configErrorProvider{err: err}
}

func (p *configErrorProvider) configError(operation string) error {
	return core.NewInvalidRequestError(fmt.Sprintf("opencodezen %s unavailable: %v", operation, p.err), nil)
}

func (p *configErrorProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, p.configError("chat completions")
}

func (p *configErrorProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, p.configError("streaming chat completions")
}

func (p *configErrorProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, p.configError("responses")
}

func (p *configErrorProvider) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, p.configError("streaming responses")
}

func (p *configErrorProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	return nil, p.configError("model listing")
}

func (p *configErrorProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, p.configError("embeddings")
}

func (p *configErrorProvider) Passthrough(context.Context, *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return nil, p.configError("passthrough")
}

func (p *configErrorProvider) ProviderName() string { return "opencodezen" }
