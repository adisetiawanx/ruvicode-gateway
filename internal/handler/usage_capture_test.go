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

// TestUsageCaptureParsesUpstreamCost matches the real provider final chunk,
// which reports the upstream settlement cost alongside usage. It is captured
// for internal margin accounting only.
func TestUsageCaptureParsesUpstreamCost(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"id":"chatcmpl-1","usage":{"prompt_tokens":10,"completion_tokens":36,"completion_tokens_details":{"reasoning_tokens":33}},"cost":{"usd":0.00001073,"diem":0}}`)

	uc.ParseFromChunk(line)

	if uc.UpstreamCost != 0.00001073 {
		t.Fatalf("expected upstream cost 0.00001073, got %f", uc.UpstreamCost)
	}
	if uc.Usage == nil || uc.Usage.ReasoningTokens != 33 {
		t.Fatalf("expected reasoning tokens 33 captured, got %+v", uc.Usage)
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

// TestUsageCaptureParsesCurrentCostShape matches the provider's current
// final-chunk shape: cost is a scalar (0 when byok) and the real settlement
// number lives in cost_details.upstream_inference_cost.
func TestUsageCaptureParsesCurrentCostShape(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"id":"chatcmpl-1","usage":{"prompt_tokens":84,"completion_tokens":41,"completion_tokens_details":{"reasoning_tokens":31}},"cost":0,"is_byok":true,"cost_details":{"upstream_inference_cost":0.00002324,"upstream_inference_prompt_cost":0.00001176,"upstream_inference_completions_cost":0.00001148}}`)

	uc.ParseFromChunk(line)

	if uc.UpstreamCost != 0.00002324 {
		t.Fatalf("expected upstream cost 0.00002324, got %f", uc.UpstreamCost)
	}
	if uc.Usage == nil || uc.Usage.PromptTokens != 84 || uc.Usage.CompletionTokens != 41 {
		t.Fatalf("expected usage 84/41 captured, got %+v", uc.Usage)
	}
}

// TestUsageCaptureScalarCostStillWorks covers the plain scalar cost shape
// without cost_details present.
func TestUsageCaptureScalarCostStillWorks(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"id":"chatcmpl-1","usage":{"prompt_tokens":5,"completion_tokens":5},"cost":0.00000123}`)

	uc.ParseFromChunk(line)

	if uc.UpstreamCost != 0.00000123 {
		t.Fatalf("expected upstream cost 0.00000123, got %f", uc.UpstreamCost)
	}
}
