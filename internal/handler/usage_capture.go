package handler

import (
	"encoding/json"
	"strings"

	"github.com/ruvicode/gateway/internal/provider"
)

// UsageCapture extracts usage and the upstream-reported cost from the final
// SSE chunk of a streaming response. It is populated by the streaming proxy
// loop so billing can record both the user charge and the internal upstream
// cost for margin accounting.
type UsageCapture struct {
	Usage        *provider.Usage
	UpstreamCost float64
}

// ParseFromChunk parses an SSE "data: {...}" line and captures usage and the
// upstream cost field if present. The raw cost object is parsed on purpose:
// the version of the line forwarded to the user has the cost stripped (see
// masking.StripCostField), so this parser is the only place the streaming
// path still sees it.
func (u *UsageCapture) ParseFromChunk(line []byte) {
	str := string(line)
	idx := strings.Index(str, "data: ")
	if idx == -1 {
		return
	}

	jsonStr := strings.TrimSpace(str[idx+len("data: "):])

	var chunk struct {
		Usage struct {
			PromptTokens     int             `json:"prompt_tokens"`
			CompletionTokens int             `json:"completion_tokens"`
			Details          json.RawMessage `json:"completion_tokens_details"`
		} `json:"usage"`
		Cost struct {
			USD float64 `json:"usd"`
		} `json:"cost"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
		return
	}

	if chunk.Cost.USD > 0 {
		u.UpstreamCost = chunk.Cost.USD
	}

	if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		usage := &provider.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
		}
		if len(chunk.Usage.Details) > 0 {
			var reason struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			}
			_ = json.Unmarshal(chunk.Usage.Details, &reason)
			usage.ReasoningTokens = reason.ReasoningTokens
		}
		u.Usage = usage
	}
}