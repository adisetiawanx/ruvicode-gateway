// Package provider defines the provider abstraction for the Ruvicode gateway.
//
// The gateway core talks only to the Provider interface. Concrete upstream
// providers implement this interface and are registered in a Registry. The
// upstream identity is deliberately masked: we always say "provider", never
// the specific upstream vendor, so that end users can never discover which
// provider serves their requests.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// DefaultName is the identifier of the default (sole) provider for MVP.
// It matches the generic "provider" naming used in config, env vars, and the
// model_prices.provider column, keeping the upstream identity masked.
const DefaultName = "provider"

// Message is a single chat message in a provider-agnostic form.
// Content can be a plain string or an array of content blocks.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // Can be string or array of content blocks

	// Tool-call fields are forwarded verbatim so agentic conversations
	// survive the hop: without them the upstream sees tool results that
	// reference nothing and rejects the request.
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall mirrors the OpenAI function-tool-call shape on an assistant
// message.
type ToolCall struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function ToolCallFn `json:"function"`
}

// ToolCallFn is the function part of a tool call.
type ToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatRequest is the provider-agnostic request the gateway core builds after
// auth, rate limit, and billing pre-checks. Provider-specific fields are NOT
// forwarded upstream.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Stream      bool
	MaxTokens   *int
	Temperature *float64
	Tools       json.RawMessage // Provider may translate format
	UserID      string          // For logging/metering, NOT forwarded upstream
	APIKeyID    string          // For logging/metering
}

// Usage holds token counts extracted from a provider response.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}

// ChatResult is the provider's response, either streaming or complete.
type ChatResult struct {
	// For non-streaming: the full response body.
	Body []byte

	// For streaming: a reader the gateway proxies to the client.
	Stream io.ReadCloser

	// Usage data (always available for non-streaming; for streaming the
	// gateway parses it from the final SSE chunk).
	Usage *Usage

	// UpstreamCost is what the provider charged Ruvicode, used for internal
	// margin accounting only. Never exposed to the user.
	UpstreamCost float64

	ProviderName string
	Latency      time.Duration
}

// PricingData represents a model's pricing from a provider.
type PricingData struct {
	Model              string
	DisplayName        string
	RefInputPer1M      float64 // OpenRouter reference price
	RefOutputPer1M     float64
	ProviderInputPer1M float64 // This provider's price
	ProviderOutputPer1M float64
	DiscountPct        float64 // Discount vs reference
}

// ModelInfo represents a model's metadata.
type ModelInfo struct {
	ID            string
	DisplayName   string
	ContextWindow int
	Capabilities  []string
}

// Provider is the interface all upstream providers implement.
// The gateway core calls this interface, never a concrete implementation.
type Provider interface {
	// Name returns the provider's registry identifier (e.g. "provider").
	Name() string

	// ChatCompletion forwards a request to the upstream provider and returns
	// either a streaming reader or a complete response body.
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResult, error)

	// ListModels returns all models available from this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// FetchPricing returns current pricing data from this provider.
	FetchPricing(ctx context.Context) ([]PricingData, error)

	// HealthCheck returns nil if the provider is healthy, an error otherwise.
	HealthCheck(ctx context.Context) error
}

// ProviderError is a sanitized error the gateway translates into a
// user-facing OpenAI-compatible error (ADR-022). RawBody is internal only and
// is NEVER forwarded to the user.
type ProviderError struct {
	StatusCode int
	RawBody    string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error: %d", e.StatusCode)
}

// ErrNoKeysAvailable is returned when the provider key pool is exhausted.
var ErrNoKeysAvailable = fmt.Errorf("no provider keys available")
