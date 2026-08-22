package handler

import "testing"

func TestUsageCaptureParsesCacheReadFromFinalChunk(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"id":"x","choices":[],"usage":{"prompt_tokens":5075,"completion_tokens":15,"prompt_cache_hit_tokens":5184,"prompt_cache_miss_tokens":39,"prompt_cache_write_tokens":0,"cached_tokens":5184}}`)

	uc.ParseFromChunk(line)

	if uc.Usage == nil {
		t.Fatal("usage not captured")
	}
	if uc.Usage.PromptTokens != 5075 {
		t.Errorf("prompt = %d, want 5075", uc.Usage.PromptTokens)
	}
	if uc.Usage.CacheReadTokens != 5184 {
		t.Errorf("cache read = %d, want 5184", uc.Usage.CacheReadTokens)
	}
}

func TestUsageCaptureFallsBackToPromptDetails(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}}`)

	uc.ParseFromChunk(line)

	if uc.Usage == nil || uc.Usage.CacheReadTokens != 80 {
		t.Fatalf("cache read from prompt_details = %+v, want 80", uc.Usage)
	}
}

func TestUsageCaptureNoCacheFields(t *testing.T) {
	uc := &UsageCapture{}
	line := []byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":5}}`)

	uc.ParseFromChunk(line)

	if uc.Usage == nil || uc.Usage.CacheReadTokens != 0 {
		t.Fatalf("cache read should be 0, got %+v", uc.Usage)
	}
}
