// Package probe1085 is a throwaway local probe for ENTERPILOT/GoModel#1085.
// It exercises the full chain Messages request -> chat -> Responses-only
// upstream (chatgpt provider) -> chat chunks -> Messages SSE events, with a
// fake Codex backend, and reports what survives the double translation.
// NOT for commit — local verification only.
package probe1085

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/chatgpt"
)

// fakeCodex replies with a canned Responses SSE stream and captures the
// request body the gateway sent upstream.
func fakeCodex(t *testing.T, events string) (*httptest.Server, *strings.Builder) {
	t.Helper()
	var captured strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.WriteString(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, events)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func newChatGPT(t *testing.T, srv *httptest.Server) core.Provider {
	t.Helper()
	p := chatgpt.New(providers.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL}, providers.ProviderOptions{})
	return p
}

// anthropicRequest is a typical Claude Code style request: thinking on,
// temperature set, max_tokens, a tool, and a multi-turn history with
// thinking + tool_use + tool_result.
const anthropicRequest = `{
  "model": "gpt-5.5",
  "max_tokens": 4096,
  "stream": true,
  "temperature": 0.7,
  "system": "You are a helpful assistant.",
  "thinking": {"type": "enabled", "budget_tokens": 10000},
  "tools": [{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}}],
  "messages": [
    {"role": "user", "content": "What is the weather in Paris?"},
    {"role": "assistant", "content": [
      {"type": "thinking", "thinking": "The user wants weather; I should call the tool.", "signature": "sig-abc"},
      {"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris"}}
    ]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny, 22C"}]}
  ]
}`

// codexStreamWithReasoning mimics a Codex Responses SSE stream: reasoning
// item, then text, then a function call, then completed with usage.
const codexStreamWithReasoning = `data: {"type":"response.created","response":{"id":"resp_1","created_at":1730000000,"model":"gpt-5.5"}}

data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}

data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Checking the tool result. "}

data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"It is sunny."}

data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"The weather in Paris is sunny, 22C."}

data: {"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":""}}

data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"city\":\"Paris\"}"}

data: {"type":"response.completed","response":{"id":"resp_1","created_at":1730000000,"model":"gpt-5.5","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Checking the tool result. It is sunny."}]},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"The weather in Paris is sunny, 22C."}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}],"usage":{"input_tokens":120,"output_tokens":40,"total_tokens":160}}}

`

// parseMessagesSSE collects the Anthropic SSE events the stream converter emits.
func parseMessagesSSE(t *testing.T, body io.Reader) []map[string]any {
	t.Helper()
	var events []map[string]any
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events
}

func TestProbe_RequestPath(t *testing.T) {
	srv, captured := fakeCodex(t, codexStreamWithReasoning)
	p := newChatGPT(t, srv)

	mreq, err := anthropicapi.DecodeMessagesRequest([]byte(anthropicRequest))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	chatReq, err := anthropicapi.ToChatRequest(mreq)
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}

	t.Logf("chat request: MaxTokens=%v Temperature=%v Reasoning=%+v", *chatReq.MaxTokens, *chatReq.Temperature, chatReq.Reasoning)

	stream, err := p.StreamChatCompletion(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	defer stream.Close()
	if chatReq.StreamOptions == nil || !chatReq.StreamOptions.IncludeUsage {
		t.Log("NOTE: StreamOptions.IncludeUsage false — usage chunk not requested")
	} else {
		t.Log("IncludeUsage=true was requested")
	}

	t.Log("=== upstream request body (what Codex sees) ===")
	var up map[string]any
	_ = json.Unmarshal([]byte(captured.String()), &up)
	pretty, _ := json.MarshalIndent(up, "", "  ")
	t.Logf("%s", pretty)
	for _, field := range []string{"max_output_tokens", "temperature", "top_p", "reasoning"} {
		if _, ok := up[field]; ok {
			t.Logf("KEPT upstream: %s = %v", field, up[field])
		} else {
			t.Logf("DROPPED upstream: %s", field)
		}
	}

	// Responses stream -> chat chunks happens inside StreamChatViaResponses;
	// now feed the chat chunk stream through the Messages SSE converter.
	conv := anthropicapi.NewStreamConverter(stream, chatReq.Model, 100)
	defer conv.Close()
	events := parseMessagesSSE(t, conv)

	t.Log("=== Messages SSE events out ===")
	hasThinking, hasText, hasToolUse, hasSig := false, false, false, false
	for _, ev := range events {
		b, _ := json.Marshal(ev)
		t.Logf("%s", b)
		s := string(b)
		if strings.Contains(s, "thinking_delta") {
			hasThinking = true
		}
		if strings.Contains(s, "text_delta") {
			hasText = true
		}
		if strings.Contains(s, "tool_use") {
			hasToolUse = true
		}
		if strings.Contains(s, "signature_delta") {
			hasSig = true
		}
	}
	t.Logf("RESULT: thinking_blocks=%v text=%v tool_use=%v signature=%v", hasThinking, hasText, hasToolUse, hasSig)
	if !hasThinking {
		t.Error("LOSS: reasoning deltas from Codex never became Anthropic thinking blocks")
	}
	if !hasText {
		t.Error("LOSS: output text missing")
	}
	if !hasToolUse {
		t.Error("LOSS: tool call missing")
	}
}

func TestProbe_NonStreaming(t *testing.T) {
	srv, _ := fakeCodex(t, codexStreamWithReasoning)
	p := newChatGPT(t, srv)

	mreq, _ := anthropicapi.DecodeMessagesRequest([]byte(strings.Replace(anthropicRequest, `"stream": true,`, `"stream": false,`, 1)))
	chatReq, err := anthropicapi.ToChatRequest(mreq)
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	chatReq.Stream = false

	resp, err := p.ChatCompletion(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	out := anthropicapi.FromChatResponse(resp)
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("=== Messages response out ===\n%s", b)

	hasThinking := false
	for _, block := range out.Content {
		if block.Type == "thinking" || block.Type == "redacted_thinking" {
			hasThinking = true
		}
	}
	if !hasThinking {
		t.Error("LOSS: reasoning item from Codex never became an Anthropic thinking block (non-streaming)")
	}
}
