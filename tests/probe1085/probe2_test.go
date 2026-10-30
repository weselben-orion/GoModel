package probe1085

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
)

func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

func io_NopCloser(b []byte) io.ReadCloser { return io.NopCloser(strings.NewReader(string(b))) }

// Probe 2a: streaming usage — does message_delta carry real output_tokens?
func TestProbe_StreamingUsage(t *testing.T) {
	srv, _ := fakeCodex(t, codexStreamWithReasoning)
	p := newChatGPT(t, srv)

	mreq, _ := anthropicapi.DecodeMessagesRequest([]byte(anthropicRequest))
	chatReq, _ := anthropicapi.ToChatRequest(mreq)

	stream, err := p.StreamChatCompletion(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	defer stream.Close()

	// dump raw chat chunks to see whether the usage chunk exists at all
	raw, _ := readAll(stream)
	t.Log("=== raw chat chunks ===")
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "data:") {
			t.Log(line)
		}
	}

	conv := anthropicapi.NewStreamConverter(io_NopCloser(raw), chatReq.Model, 100)
	defer conv.Close()
	events := parseMessagesSSE(t, conv)
	for _, ev := range events {
		if ev["type"] == "message_delta" {
			b, _ := json.Marshal(ev)
			t.Logf("message_delta: %s", b)
			usage, _ := ev["usage"].(map[string]any)
			if usage["output_tokens"] == float64(0) {
				t.Error("LOSS: message_delta usage.output_tokens = 0 despite upstream usage 40")
			}
		}
	}
}

// Probe 2b: multi-turn replay — the assistant turn (text + tool_use, plus
// thinking the client kept) goes back upstream. What happens to reasoning?
func TestProbe_ReplayReasoningUpstream(t *testing.T) {
	srv, captured := fakeCodex(t, codexStreamSimple)
	p := newChatGPT(t, srv)

	// Second turn: client sends the assistant's previous turn back, including
	// the unsigned thinking block the gateway emitted on turn 1.
	secondTurn := `{
	  "model": "gpt-5.5",
	  "max_tokens": 4096,
	  "thinking": {"type": "enabled", "budget_tokens": 10000},
	  "messages": [
	    {"role": "user", "content": "Hi"},
	    {"role": "assistant", "content": [
	      {"type": "thinking", "thinking": "some reasoning from turn 1", "signature": ""},
	      {"type": "text", "text": "Hello there."}
	    ]},
	    {"role": "user", "content": "Follow-up question."}
	  ]
	}`
	mreq, err := anthropicapi.DecodeMessagesRequest([]byte(secondTurn))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	chatReq, err := anthropicapi.ToChatRequest(mreq)
	if err != nil {
		t.Fatalf("ToChatRequest: %v", err)
	}
	stream, err := p.StreamChatCompletion(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("StreamChatCompletion: %v", err)
	}
	defer stream.Close()
	_, _ = readAll(stream)

	var up map[string]any
	_ = json.Unmarshal([]byte(captured.String()), &up)
	pretty, _ := json.MarshalIndent(up, "", "  ")
	t.Logf("=== second-turn upstream body ===\n%s", pretty)

	// Check: did the assistant turn carry a reasoning item back?
	input, _ := up["input"].([]any)
	foundReasoning := false
	for _, item := range input {
		m, _ := item.(map[string]any)
		if m["type"] == "reasoning" {
			foundReasoning = true
		}
	}
	t.Logf("reasoning item replayed upstream: %v", foundReasoning)
}

const codexStreamSimple = `data: {"type":"response.created","response":{"id":"resp_2","created_at":1730000000,"model":"gpt-5.5"}}

data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"OK."}

data: {"type":"response.completed","response":{"id":"resp_2","created_at":1730000000,"model":"gpt-5.5","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"OK."}]}],"usage":{"input_tokens":50,"output_tokens":5,"total_tokens":55}}}

`
