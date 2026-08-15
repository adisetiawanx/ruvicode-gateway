package handler

import (
	"encoding/json"
	"testing"
)

// Agentic clients (OpenAI SDK, OpenCode) send assistant messages whose
// content is null while tool_calls carry the actual payload. Some upstreams
// reject that combination; parseMessages must normalize it to "".
func TestParseMessagesNormalizesNullContentWithToolCalls(t *testing.T) {
	raw := json.RawMessage(`[
		{"role":"user","content":"buatkan website"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"},
		{"role":"user","content":"lanjut"}
	]`)

	msgs := parseMessages(raw)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}

	assistant := msgs[1]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call forwarded, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].Function.Name != "write" {
		t.Errorf("tool name not forwarded: %q", assistant.ToolCalls[0].Function.Name)
	}

	var content string
	if err := json.Unmarshal(assistant.Content, &content); err != nil {
		t.Fatalf("content is not a JSON string after normalization: %v (%s)", err, assistant.Content)
	}
	if content != "" {
		t.Errorf("expected empty string content, got %q", content)
	}

	tool := msgs[2]
	if tool.ToolCallID != "call_1" {
		t.Errorf("tool_call_id not forwarded: %q", tool.ToolCallID)
	}
}

// A plain null content without tool_calls must stay untouched (the upstream
// accepts that shape on its own).
func TestParseMessagesKeepsNullContentWithoutToolCalls(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":"hi"},{"role":"assistant","content":null},{"role":"user","content":"lanjut"}]`)
	msgs := parseMessages(raw)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if string(msgs[1].Content) != "null" {
		t.Errorf("content without tool_calls should pass through, got %s", msgs[1].Content)
	}
}
