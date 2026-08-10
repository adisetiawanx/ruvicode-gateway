package handler

import (
	"encoding/json"
	"strings"

	"github.com/ruvicode/gateway/internal/provider"
)

// UsageCapture extracts token usage from the final SSE chunk of a streaming
// response. It is populated by the streaming proxy loop.
type UsageCapture struct {
	Usage *provider.Usage
}

// ParseFromChunk parses an SSE "data: {...}" line and captures usage if present.
func (u *UsageCapture) ParseFromChunk(line []byte) {
	str := string(line)
	idx := strings.Index(str, "data: ")
	if idx == -1 {
		return
	}

	jsonStr := strings.TrimSpace(str[idx+len("data: "):])

	var chunk struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
		return
	}

	if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		u.Usage = &provider.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
		}
	}
}
