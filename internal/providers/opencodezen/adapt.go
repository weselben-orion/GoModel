package opencodezen

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

// gateTools are the 13 tool names the OpenCode CLI sends (wire capture,
// 2026-11-12: edit, experimental_image_generate, glob, grep, question, read,
// shell, skill, subagent, webfetch, websearch, write, execute — shell, not
// bash). The gate content-matches this array, so requests are padded to it
// exactly, in this order.
var gateTools = []map[string]any{
	toolStub("edit"), toolStub("experimental_image_generate"), toolStub("glob"),
	toolStub("grep"), toolStub("question"), toolStub("read"), toolStub("shell"),
	toolStub("skill"), toolStub("subagent"), toolStub("webfetch"),
	toolStub("websearch"), toolStub("write"), toolStub("execute"),
}

// toolStub renders one gate tool entry. The schema is an empty object: the
// stub is inert — there is nothing on the GoModel side for the model to call
// through it.
func toolStub(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": "OpenCode workspace tool.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// adaptChatRequest returns the AdaptChatRequest hook: it forces stream:true
// (the free tier rejects stream:false), pads the tools array to the gate's
// 13-tool list, and prepends the gate-prompt block. It never mutates its
// argument; on a disabled gate it returns the request untouched.
func adaptChatRequest(g *gate) func(*core.ChatRequest) (*core.ChatRequest, error) {
	return func(req *core.ChatRequest) (*core.ChatRequest, error) {
		return adaptGateRequest(g, req), nil
	}
}

// adaptGateRequest applies the gate shaping to req: stream forced on, tools
// padded to the 13-tool array, gate block prepended. The request is copied
// before any mutation.
func adaptGateRequest(g *gate, req *core.ChatRequest) *core.ChatRequest {
	if g == nil || g.block == "" {
		return req
	}
	out := *req
	out.Stream = true
	out.Tools = ensureGateTools(req.Tools)
	out.Messages = g.injectGatePrompt(req.Messages)
	return &out
}

// ensureGateTools pads tools with the gate's 13-tool array. Caller tools
// keep their leading positions (indices stay stable); every gate name is
// present at least once.
func ensureGateTools(tools []map[string]any) []map[string]any {
	existing := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		existing[toolName(t)] = struct{}{}
	}
	out := make([]map[string]any, 0, len(tools)+len(gateTools))
	out = append(out, tools...)
	for _, stub := range gateTools {
		if _, ok := existing[toolName(stub)]; !ok {
			out = append(out, stub)
		}
	}
	return out
}

// toolName extracts the function name from an OpenAI-shaped tool entry.
func toolName(tool map[string]any) string {
	fn, _ := tool["function"].(map[string]any)
	name, _ := fn["name"].(string)
	return name
}

// chunkToolCall is one streamed tool-call fragment. The wire shape carries an
// index the core ToolCall type does not model, so the fold reads it here.
type chunkToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// foldChatSSE collects the upstream chat.completion.chunk SSE stream into a
// single buffered chat.completion. The free tier only answers stream:true,
// so a non-streaming caller is served by folding the SSE the upstream
// returns: content, reasoning_content, tool_calls (accumulated per index),
// and the usage chunk are assembled into one response.
func foldChatSSE(stream io.Reader, model string) (*core.ChatResponse, error) {
	out := &core.ChatResponse{
		ID:      "chatcmpl-" + base62(12),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
	}
	toolCalls := map[int]*core.ToolCall{}
	reasoning := strings.Builder{}

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			ID      string      `json:"id"`
			Created int64       `json:"created"`
			Usage   *core.Usage `json:"usage"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string          `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					ToolCalls        []chunkToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("opencodezen: decode SSE chunk: %w", err)
		}
		if chunk.ID != "" {
			out.ID = chunk.ID
		}
		if chunk.Created != 0 {
			out.Created = chunk.Created
		}
		if chunk.Usage != nil {
			out.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		c := chunk.Choices[0]
		d := c.Delta
		if len(out.Choices) == 0 {
			out.Choices = []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant"}}}
		}
		msg, _ := out.Choices[0].Message.Content.(string)
		out.Choices[0].Message.Content = msg + d.Content
		reasoning.WriteString(d.ReasoningContent)
		for _, tc := range d.ToolCalls {
			cur := toolCalls[tc.Index]
			if cur == nil {
				cur = &core.ToolCall{Type: "function", ID: tc.ID}
				toolCalls[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			cur.Function.Name += tc.Function.Name
			cur.Function.Arguments += tc.Function.Arguments
		}
		if c.FinishReason != "" {
			out.Choices[0].FinishReason = c.FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("opencodezen: read SSE stream: %w", err)
	}

	if len(out.Choices) > 0 {
		if reasoning.Len() > 0 {
			out.Choices[0].Message.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_content": mustJSON(reasoning.String()),
			})
		}
		if len(toolCalls) > 0 {
			out.Choices[0].Message.ToolCalls = sortedToolCalls(toolCalls)
			if out.Choices[0].FinishReason == "" || out.Choices[0].FinishReason == "stop" {
				out.Choices[0].FinishReason = "tool_calls"
			}
		}
	}
	return out, nil
}

// mustJSON renders s as JSON; strings never fail to marshal.
func mustJSON(s string) json.RawMessage {
	data, _ := json.Marshal(s)
	return data
}

// sortedToolCalls flattens the accumulated per-index tool calls in index
// order.
func sortedToolCalls(toolCalls map[int]*core.ToolCall) []core.ToolCall {
	out := make([]core.ToolCall, 0, len(toolCalls))
	for i := 0; i < len(toolCalls); i++ {
		if tc, ok := toolCalls[i]; ok {
			out = append(out, *tc)
		}
	}
	return out
}
