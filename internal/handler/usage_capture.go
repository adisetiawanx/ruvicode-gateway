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
			PromptDetails    json.RawMessage `json:"prompt_tokens_details"`
			CacheHitTokens   int             `json:"prompt_cache_hit_tokens"`
			CacheReadTokens  int             `json:"cache_read_input_tokens"`
			CachedTokens     int             `json:"cached_tokens"`
			Cost             json.RawMessage `json:"cost"`
			CostDetails      *costDetailsObj `json:"cost_details"`
		} `json:"usage"`
		Cost        json.RawMessage `json:"cost"`
		CostDetails *costDetailsObj `json:"cost_details"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
		return
	}

	// Current upstream shape nests cost inside usage; older shapes put it at
	// the top level of the chunk.
	upstream := parseChunkUpstreamCost(chunk.Usage.Cost, chunk.Usage.CostDetails)
	if upstream == 0 {
		upstream = parseChunkUpstreamCost(chunk.Cost, chunk.CostDetails)
	}
	if upstream > 0 {
		u.UpstreamCost = upstream
	}

	if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		usage := &provider.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			CacheReadTokens:  provider.ParseCacheReadTokens(chunk.Usage.CacheHitTokens, chunk.Usage.CacheReadTokens, chunk.Usage.CachedTokens, chunk.Usage.PromptDetails),
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

// costDetailsObj mirrors the settlement detail fields the upstream reports
// in the final SSE chunk.
type costDetailsObj struct {
	UpstreamInferenceCost float64 `json:"upstream_inference_cost"`
}

// parseChunkUpstreamCost accepts all observed cost field shapes:
// {"cost":{"usd":x}} (legacy), {"cost":x} (scalar), and
// {"cost_details":{"upstream_inference_cost":x}} (current).
func parseChunkUpstreamCost(cost json.RawMessage, details *costDetailsObj) float64 {
	if len(cost) > 0 && string(cost) != "null" {
		var obj struct {
			USD float64 `json:"usd"`
		}
		if err := json.Unmarshal(cost, &obj); err == nil && obj.USD > 0 {
			return obj.USD
		}
		var scalar float64
		if err := json.Unmarshal(cost, &scalar); err == nil && scalar > 0 {
			return scalar
		}
	}
	if details != nil && details.UpstreamInferenceCost > 0 {
		return details.UpstreamInferenceCost
	}
	return 0
}
