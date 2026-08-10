package handler

import (
	"testing"
)

func TestUsageCaptureParsesFinalChunk(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","usage":{"prompt_tokens":18,"completion_tokens":50}}`)

	uc.ParseFromChunk(line)

	if uc.Usage == nil {
		t.Fatal("expected usage captured")
	}
	if uc.Usage.PromptTokens != 18 || uc.Usage.CompletionTokens != 50 {
		t.Fatalf("expected usage 18/50, got %+v", uc.Usage)
	}
}

func TestUsageCaptureIgnoresContentChunks(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hello"}}]}`)

	uc.ParseFromChunk(line)

	if uc.Usage != nil {
		t.Fatalf("expected no usage from content chunk, got %+v", uc.Usage)
	}
}

func TestUsageCaptureIgnoresNonDataLine(t *testing.T) {
	uc := &UsageCapture{}
	uc.ParseFromChunk([]byte(": keep-alive comment"))

	if uc.Usage != nil {
		t.Fatal("expected no usage from non-data line")
	}
}
