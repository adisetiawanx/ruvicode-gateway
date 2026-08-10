package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProviderClient is the concrete Provider implementation for the MVP's sole
// upstream. It talks to an OpenAI-compatible surface and injects
// stream_options so usage data is always available for billing.
//
// The upstream identity is masked: Name() returns DefaultName ("provider"),
// and the client never leaks key material, provider names, or URLs to callers.
type ProviderClient struct {
	baseURL string
	keys    *KeyPool
	client  *http.Client
}

// NewClient builds a ProviderClient for the given base URL and API keys.
func NewClient(baseURL string, apiKeys []string) *ProviderClient {
	return &ProviderClient{
		baseURL: baseURL,
		keys:    NewKeyPool(apiKeys),
		client: &http.Client{
			Timeout: 300 * time.Second, // Long for streaming.
		},
	}
}

// Name returns the registry identifier for this provider.
func (p *ProviderClient) Name() string {
	return DefaultName
}

// ChatCompletion forwards a request to the upstream provider and returns
// either a streaming reader or a complete response body.
func (p *ProviderClient) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResult, error) {
	key := p.keys.Select()
	if key == "" {
		return nil, ErrNoKeysAvailable
	}

	body := map[string]interface{}{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   req.Stream,
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = json.RawMessage(req.Tools)
	}

	// Inject stream_options.include_usage so billing always has usage data.
	if req.Stream {
		body["stream_options"] = map[string]bool{"include_usage": true}
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.baseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Ruvicode-Gateway/1.0")

	start := time.Now()
	httpResp, err := p.client.Do(httpReq)
	latency := time.Since(start)

	if err != nil {
		p.keys.MarkError(key)
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		errorBody, _ := io.ReadAll(httpResp.Body)
		return nil, &ProviderError{
			StatusCode: httpResp.StatusCode,
			RawBody:    string(errorBody),
		}
	}

	// Record rate-limit state from response headers.
	p.keys.UpdateFromHeaders(key, httpResp.Header)

	result := &ChatResult{
		Stream:       httpResp.Body,
		ProviderName: p.Name(),
		Latency:      latency,
	}

	if !req.Stream {
		defer httpResp.Body.Close()
		bodyBytes, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		result.Body = bodyBytes
		result.Usage = parseUsage(bodyBytes)
	}

	return result, nil
}

// parseUsage extracts token usage from an OpenAI-compatible response body.
func parseUsage(body []byte) *Usage {
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			ReasoningTokens  int `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	if resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
		return nil
	}
	return &Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}
}

// ListModels returns all models available from this provider.
func (p *ProviderClient) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := p.baseURL + "/v1/models"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID              string   `json:"id"`
			Name            string   `json:"name"`
			ContextLength   int      `json:"context_length"`
			SupportedParams []string `json:"supported_parameters"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, len(result.Data))
	for i, m := range result.Data {
		models[i] = ModelInfo{
			ID:            m.ID,
			DisplayName:   m.Name,
			ContextWindow: m.ContextLength,
			Capabilities:  m.SupportedParams,
		}
	}
	return models, nil
}

// FetchPricing returns current pricing data from this provider.
func (p *ProviderClient) FetchPricing(ctx context.Context) ([]PricingData, error) {
	url := p.baseURL + "/api/markets"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch pricing: unexpected status %d", resp.StatusCode)
	}

	var raw struct {
		Markets []struct {
			Model             string  `json:"model"`
			BestInputPer1M    float64 `json:"best_input_per_1m"`
			BestOutputPer1M   float64 `json:"best_output_per_1m"`
			DirectInputPer1M  float64 `json:"direct_input_per_1m"`
			DirectOutputPer1M float64 `json:"direct_output_per_1m"`
		} `json:"markets"`
		Data []struct {
			Model             string  `json:"model"`
			BestInputPer1M    float64 `json:"best_input_per_1m"`
			BestOutputPer1M   float64 `json:"best_output_per_1m"`
			DirectInputPer1M  float64 `json:"direct_input_per_1m"`
			DirectOutputPer1M float64 `json:"direct_output_per_1m"`
		} `json:"data"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	list := raw.Markets
	if list == nil {
		list = raw.Data
	}

	prices := make([]PricingData, 0, len(list))
	for _, m := range list {
		var discountPct float64
		if m.DirectInputPer1M > 0 {
			discountPct = (1 - m.BestInputPer1M/m.DirectInputPer1M) * 100
		}
		prices = append(prices, PricingData{
			Model:               m.Model,
			RefInputPer1M:       m.DirectInputPer1M / 1_000_000,
			RefOutputPer1M:      m.DirectOutputPer1M / 1_000_000,
			ProviderInputPer1M:  m.BestInputPer1M / 1_000_000,
			ProviderOutputPer1M: m.BestOutputPer1M / 1_000_000,
			DiscountPct:         discountPct,
		})
	}
	return prices, nil
}

// HealthCheck returns nil if the provider is healthy, an error otherwise.
func (p *ProviderClient) HealthCheck(ctx context.Context) error {
	_, err := p.ListModels(ctx)
	return err
}
