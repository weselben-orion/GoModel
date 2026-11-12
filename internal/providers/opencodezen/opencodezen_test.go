package opencodezen

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateBlock is a stand-in gate prompt: over minGateBytes, distinctive
// enough to assert byte-identical injection.
var gateBlock = strings.Repeat("You are an AI agent running in OpenCode, a coding agent harness. ", 30)

func testGate(t *testing.T) *gate {
	t.Helper()
	return &gate{block: gateBlock}
}

func TestRegistration(t *testing.T) {
	assert.Equal(t, "opencodezen", Registration.Type)
	require.NotNil(t, Registration.New)
	assert.Equal(t, "https://opencode.ai/inference/openai/v1", Registration.Discovery.DefaultBaseURL)
}

func TestNew_GateFileTooShortFailsAtBoot(t *testing.T) {
	short := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(short, []byte("too short")))
	t.Setenv(gateFileEnvVar, short)

	provider := New(providers.ProviderConfig{APIKey: "k"}, providertest.Options(llmclient.Hooks{}))
	require.NotNil(t, provider)
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hi"}}})
	require.Error(t, err)
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
	assert.Contains(t, gwErr.Error(), "truncated or stale capture")
}

func TestNew_MissingGateFileFailsAtBoot(t *testing.T) {
	t.Setenv(gateFileEnvVar, t.TempDir()+"/does-not-exist.txt")

	provider := New(providers.ProviderConfig{APIKey: "k"}, providertest.Options(llmclient.Hooks{}))
	_, err := provider.ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read gate file")
}

func TestNew_GateFileValid(t *testing.T) {
	gateFile := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(gateFile, []byte(gateBlock)))
	t.Setenv(gateFileEnvVar, gateFile)

	provider := New(providers.ProviderConfig{APIKey: "k", BaseURL: "http://127.0.0.1:1"}, providertest.Options(llmclient.Hooks{}))
	require.NotNil(t, provider)
	assert.Equal(t, gateBlock, provider.(*Provider).gate.block)
}

func TestGatePromptInjected(t *testing.T) {
	g := testGate(t)
	req := &core.ChatRequest{
		Model: "m",
		Messages: []core.Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
		},
	}
	adapted := adaptGateRequest(g, req)

	require.NotSame(t, req, adapted, "adapter must not mutate its argument")
	require.Len(t, adapted.Messages, 2)
	sys, ok := adapted.Messages[0].Content.(string)
	require.True(t, ok)
	assert.Equal(t, gateBlock+"\n\nbe helpful", sys, "gate block must prepend byte-identically, caller instructions kept")
	assert.Equal(t, "hi", adapted.Messages[1].Content)
}

func TestGatePromptPrependsSystemWhenAbsent(t *testing.T) {
	g := testGate(t)
	adapted := adaptGateRequest(g, &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.Len(t, adapted.Messages, 2)
	assert.Equal(t, "system", adapted.Messages[0].Role)
	assert.Equal(t, gateBlock, adapted.Messages[0].Content)
}

func TestGatePromptPrependsSystemWhenNoMessages(t *testing.T) {
	g := testGate(t)
	adapted := adaptGateRequest(g, &core.ChatRequest{Model: "m"})
	require.Len(t, adapted.Messages, 1)
	assert.Equal(t, "system", adapted.Messages[0].Role)
	assert.Equal(t, gateBlock, adapted.Messages[0].Content)
}

func TestGateDisabledLeavesRequestAlone(t *testing.T) {
	req := &core.ChatRequest{Model: "m"}
	adapted := adaptGateRequest(&gate{}, req)
	assert.Same(t, req, adapted)
}

func TestStreamForced(t *testing.T) {
	g := testGate(t)
	adapted := adaptGateRequest(g, &core.ChatRequest{Model: "m", Messages: []core.Message{{Role: "user", Content: "hi"}}})
	assert.True(t, adapted.Stream, "free tier rejects stream:false; adapter must force it on")
}

func TestCoreToolsAppended(t *testing.T) {
	g := testGate(t)
	callerTool := map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "my_tool", "description": "caller tool"},
	}
	adapted := adaptGateRequest(g, &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Tools:    []map[string]any{callerTool},
	})

	require.Len(t, adapted.Tools, len(gateTools)+1, "caller tools keep leading positions, gate tools pad to 13")
	assert.Equal(t, "my_tool", toolName(adapted.Tools[0]))
	names := make([]string, 0, len(adapted.Tools))
	for _, tool := range adapted.Tools {
		names = append(names, toolName(tool))
	}
	for _, want := range []string{"edit", "read", "shell", "write", "execute"} {
		assert.Contains(t, names, want)
	}
}

func TestCoreToolsNoDuplicates(t *testing.T) {
	g := testGate(t)
	readTool := map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "read"},
	}
	adapted := adaptGateRequest(g, &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		Tools:    []map[string]any{readTool},
	})
	count := 0
	for _, tool := range adapted.Tools {
		if toolName(tool) == "read" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a caller tool named read must not be duplicated")
}

func TestIdentityHeaders(t *testing.T) {
	t.Setenv(sessionHeadEnvVar, "ses_0123456789abcdef0123456789abcdef")
	ident := newZenIdentity(sessionHeadEnvVar)
	headers := requestHeaders(ident)(context.Background())

	assert.Equal(t, "cli", headers.Get(clientHeader))
	assert.Equal(t, "global", headers.Get(projectHeader))
	assert.Equal(t, "ses_0123456789abcdef0123456789abcdef", headers.Get(sessionHeader))
	assert.Equal(t, "ses_0123456789abcdef0123456789abcdef", headers.Get("x-session-id"))
	assert.Equal(t, inferenceUserAgent, headers.Get("User-Agent"))

	requestID := headers.Get(requestHeader)
	assert.True(t, len(requestID) > len("msg_"), "x-opencode-request present")
	assert.Equal(t, "msg_", requestID[:len("msg_")])
}

func TestRequestIDFreshPerRequest(t *testing.T) {
	ident := newZenIdentity(sessionHeadEnvVar)
	headers := requestHeaders(ident)
	first := headers(context.Background()).Get(requestHeader)
	second := headers(context.Background()).Get(requestHeader)
	assert.NotEqual(t, first, second, "stale x-opencode-request ids are rejected with 403")
}

func TestIdentityHeadersSentUpstream(t *testing.T) {
	t.Setenv(sessionHeadEnvVar, "ses_aaaa1111bbbb2222cccc3333dddd4444")
	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
	provider := New(
		providers.ProviderConfig{APIKey: "test-key", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    providertest.Model,
		Messages: []core.Message{{Role: "user", Content: providertest.Prompt}},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "cli", req.Header.Get(clientHeader))
	assert.Equal(t, "global", req.Header.Get(projectHeader))
	assert.Equal(t, "ses_aaaa1111bbbb2222cccc3333dddd4444", req.Header.Get(sessionHeader))
	assert.NotEmpty(t, req.Header.Get(requestHeader))
	assert.Equal(t, inferenceUserAgent, req.Header.Get("User-Agent"))
}

func TestUpstreamSeesForcedStreamAndGate(t *testing.T) {
	gateFile := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(gateFile, []byte(gateBlock)))
	t.Setenv(gateFileEnvVar, gateFile)

	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
	provider := New(
		providers.ProviderConfig{APIKey: "test-key", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    providertest.Model,
		Messages: []core.Message{{Role: "user", Content: providertest.Prompt}},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, true, sent["stream"], "upstream must see stream:true")
	messages, ok := sent["messages"].([]any)
	require.True(t, ok)
	first, ok := messages[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "system", first["role"])
	assert.Contains(t, first["content"], gateBlock, "gate block reaches upstream byte-identically")
	tools, ok := sent["tools"].([]any)
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(tools), len(gateTools))
}

func TestChatCompletionFoldsSSE(t *testing.T) {
	server, capture := providertest.SSEServer(t, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"lo"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`)
	provider := New(
		providers.ProviderConfig{APIKey: "k", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	assert.Equal(t, "chat.completion", resp.Object)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "hello", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 5, resp.Usage.PromptTokens)
	assert.Equal(t, 2, resp.Usage.CompletionTokens)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, true, sent["stream"], "non-streaming caller is folded, upstream still sees stream:true")
}

func TestChatCompletionFoldsToolCalls(t *testing.T) {
	sse := `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}

data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	resp, err := foldChatSSE(stringsReader(sse), "m")
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, "call_1", resp.Choices[0].Message.ToolCalls[0].ID)
	assert.Equal(t, "read", resp.Choices[0].Message.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"path":"x"}`, resp.Choices[0].Message.ToolCalls[0].Function.Arguments)
	assert.Equal(t, "tool_calls", resp.Choices[0].FinishReason)
}

func TestChatCompletionFoldsReasoningContent(t *testing.T) {
	sse := `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}

data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"answer","reasoning_content":"ing"},"finish_reason":"stop"}]}

data: [DONE]

`
	resp, err := foldChatSSE(stringsReader(sse), "m")
	require.NoError(t, err)
	content, _ := resp.Choices[0].Message.Content.(string)
	assert.Equal(t, "answer", content)
	raw := resp.Choices[0].Message.ExtraFields.Lookup("reasoning_content")
	require.NotNil(t, raw)
	assert.JSONEq(t, `"thinking"`, string(raw))
}

func TestEmbeddingsUnsupported(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "k", BaseURL: "http://127.0.0.1:1"}, providertest.Options(llmclient.Hooks{}))
	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "m", Input: "x"})
	providertest.AssertUnsupported(t, err)
}

func TestIsOpenCodeBaseURL(t *testing.T) {
	assert.True(t, isOpenCodeBaseURL(""))
	assert.True(t, isOpenCodeBaseURL("https://opencode.ai/zen/v1"))
	assert.True(t, isOpenCodeBaseURL(defaultBaseURL))
	assert.False(t, isOpenCodeBaseURL("http://127.0.0.1:8080"))
	assert.False(t, isOpenCodeBaseURL("https://example.com"))
}

func TestWithDefaultHeaders(t *testing.T) {
	defaults := http.Header{}
	defaults.Set("X-Opencode-Session", "ses_1")
	defaults.Set("X-Opencode-Request", "msg_1")

	merged := withDefaultHeaders(http.Header{"X-Opencode-Session": {"caller-wins"}}, defaults)
	assert.Equal(t, "caller-wins", merged.Get("X-Opencode-Session"), "caller header wins")
	assert.Equal(t, "msg_1", merged.Get("X-Opencode-Request"), "missing headers filled in")

	nilHeaders := withDefaultHeaders(nil, defaults)
	assert.Equal(t, "ses_1", nilHeaders.Get("X-Opencode-Session"))
}

func TestNonOpenCodeBaseURLDisablesUTLS(t *testing.T) {
	// An operator pointing the provider at a mock keeps a plain transport;
	// the gate shaping stays off until a gate file is provisioned.
	provider := New(
		providers.ProviderConfig{APIKey: "k", BaseURL: "http://127.0.0.1:1"},
		providertest.Options(llmclient.Hooks{}),
	)
	p, ok := provider.(*Provider)
	require.True(t, ok, "non-OpenCode override must not produce the config-error provider")
	assert.False(t, p.utlsActive)
	assert.False(t, p.gated)
}

func TestConfigErrorProviderFailsEverySurface(t *testing.T) {
	short := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(short, []byte("too short")))
	t.Setenv(gateFileEnvVar, short)

	provider := New(providers.ProviderConfig{APIKey: "k"}, providertest.Options(llmclient.Hooks{}))
	require.NotNil(t, provider)

	_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{})
	providertest.AssertUnsupported(t, err)

	_, err = provider.Responses(context.Background(), &core.ResponsesRequest{Model: "m"})
	providertest.AssertUnsupported(t, err)

	_, err = provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "m"})
	providertest.AssertUnsupported(t, err)

	_, err = provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "m", Input: "x"})
	providertest.AssertUnsupported(t, err)

	pt, ok := provider.(core.PassthroughProvider)
	require.True(t, ok, "config-error provider must still advertise passthrough so the failure surfaces")
	_, err = pt.Passthrough(context.Background(), &core.PassthroughRequest{})
	providertest.AssertUnsupported(t, err)

	assert.Equal(t, "opencodezen", provider.(interface{ ProviderName() string }).ProviderName())
}

func TestChatCompletionRoundTripAgainstGateFake(t *testing.T) {
	// The mock stands in for the Zen free-tier gate: it asserts the gate
	// conditions (identity headers, stream:true, gate tools, gate text) and
	// answers SSE, so the non-streaming fold is exercised end to end.
	gateFile := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(gateFile, []byte(gateBlock)))
	t.Setenv(gateFileEnvVar, gateFile)

	server, capture := newGateFake(t, gateBlock)
	provider := New(
		providers.ProviderConfig{APIKey: "k", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello from the gate fake", resp.Choices[0].Message.Content)

	sent := capture.Last(t)
	assert.Equal(t, "cli", sent.Header.Get(clientHeader))
	assert.Equal(t, "global", sent.Header.Get(projectHeader))
	assert.NotEmpty(t, sent.Header.Get(requestHeader))
	body := sent.JSON(t)
	assert.Equal(t, true, body["stream"])
	messages := body["messages"].([]any)
	first := messages[0].(map[string]any)
	assert.Equal(t, "system", first["role"])
	assert.Contains(t, first["content"], gateBlock)
	tools := body["tools"].([]any)
	assert.GreaterOrEqual(t, len(tools), len(gateTools))
}

// TestResponsesViaChatExercisesGateAdaptation verifies the Responses API
// translation reaches the upstream with the same gate shaping as chat:
// stream forced on, gate tools present, gate text in the system message.
func TestResponsesViaChatExercisesGateAdaptation(t *testing.T) {
	gateFile := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(gateFile, []byte(gateBlock)))
	t.Setenv(gateFileEnvVar, gateFile)

	server, capture := newGateFake(t, gateBlock)
	provider := New(
		providers.ProviderConfig{APIKey: "k", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "m",
		Input: "hi",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Output)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, true, sent["stream"])
	tools, ok := sent["tools"].([]any)
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(tools), len(gateTools))
	messages, ok := sent["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	first, ok := messages[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "system", first["role"])
	assert.Contains(t, first["content"], gateBlock)
}

func TestStreamResponsesRoundTripAgainstGateFake(t *testing.T) {
	gateFile := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(gateFile, []byte(gateBlock)))
	t.Setenv(gateFileEnvVar, gateFile)

	server, capture := newGateFake(t, gateBlock)
	provider := New(
		providers.ProviderConfig{APIKey: "k", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "m", Input: "hi"})
	require.NoError(t, err)
	defer stream.Close()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Contains(t, string(body), "response.output_text.delta")

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, true, sent["stream"])
}

func TestPassthroughMergesIdentityHeaders(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"ok":true}`)
	provider := New(
		providers.ProviderConfig{APIKey: "k", BaseURL: server.URL},
		providertest.Options(llmclient.Hooks{}),
	)
	pt, ok := provider.(core.PassthroughProvider)
	require.True(t, ok)

	resp, err := pt.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "/models",
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	sent := capture.Last(t)
	assert.Equal(t, "cli", sent.Header.Get(clientHeader))
	assert.Equal(t, "global", sent.Header.Get(projectHeader))
}

func TestFoldChatSSERejectsMalformedChunk(t *testing.T) {
	_, err := foldChatSSE(stringsReader("data: {not-json}\n\n"), "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode SSE chunk")
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func stringsReader(s string) io.Reader {
	return strings.NewReader(s)
}
