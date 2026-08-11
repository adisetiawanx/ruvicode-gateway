package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ruvicode/gateway/internal/billing"
	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/middleware"
	"github.com/ruvicode/gateway/internal/pricing"
	"github.com/ruvicode/gateway/internal/provider"
	"github.com/ruvicode/gateway/internal/store"
)

// ChatHandler implements POST /v1/chat/completions.
type ChatHandler struct {
	registry *provider.Registry
	billing  *billing.Engine
	pricing  *pricing.Engine
	pg       *store.PostgresStore
}

// NewChatHandler builds a ChatHandler.
func NewChatHandler(registry *provider.Registry, billing *billing.Engine, pricing *pricing.Engine, pg *store.PostgresStore) *ChatHandler {
	return &ChatHandler{registry: registry, billing: billing, pricing: pricing, pg: pg}
}

// chatRequest is the OpenAI-compatible request body.
type chatRequest struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   *int            `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
	Tools       json.RawMessage `json:"tools"`
}

// Handle runs the full request lifecycle: parse, price, pre-check, route,
// proxy, and settle.
func (h *ChatHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	keyData := middleware.GetAPIKey(ctx)

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		return
	}
	if req.Model == "" {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Model is required")
		return
	}

	// Pricing lookup (Redis cache -> Postgres fallback).
	modelPrice, err := h.pricing.GetCachedPrice(ctx, req.Model)
	if err != nil {
		masking.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "Unknown model")
		return
	}

	// Billing pre-check: estimate + hold.
	estimatedCost := h.billing.EstimateCost(modelPrice, messageCount(req.Messages), req.MaxTokens)
	preCheck, err := h.billing.PreCheck(ctx, userID, estimatedCost, keyData)
	if err != nil {
		status := http.StatusPaymentRequired
		msg := err.Error()
		if strings.Contains(msg, "temporarily unavailable") {
			status = http.StatusServiceUnavailable
		}
		masking.WriteOpenAIError(w, status, "insufficient_balance", msg)
		return
	}

	// Resolve provider for this model.
	p, err := h.registry.GetForModel(req.Model, modelPrice.Provider)
	if err != nil {
		h.billing.ReleaseHold(ctx, userID, estimatedCost)
		masking.WriteOpenAIError(w, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable")
		return
	}

	providerReq := &provider.ChatRequest{
		Model:       req.Model,
		Messages:    parseMessages(req.Messages),
		Stream:      req.Stream,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Tools:       req.Tools,
		UserID:      userID,
		APIKeyID:    keyData.KeyID,
	}

	result, err := p.ChatCompletion(ctx, providerReq)
	if err != nil {
		h.billing.ReleaseHold(ctx, userID, estimatedCost)
		masking.WriteProviderError(w, err)
		return
	}

	if req.Stream {
		h.handleStreamResponse(w, r, result, userID, keyData, modelPrice, estimatedCost, preCheck)
	} else {
		h.handleNonStreamResponse(w, r, result, userID, keyData, modelPrice, estimatedCost, preCheck)
	}
}

// handleStreamResponse proxies the SSE stream to the client while capturing
// usage from the final chunk and settling billing after the stream completes.
func (h *ChatHandler) handleStreamResponse(
	w http.ResponseWriter,
	r *http.Request,
	result *provider.ChatResult,
	userID string,
	keyData *store.APIKeyData,
	modelPrice *pricing.ModelPrice,
	estimatedCost float64,
	preCheck *billing.PreCheckResult,
) {
	requestID := middleware.RequestID(r.Context())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Ruvicode-Request-ID", requestID)
	w.Header().Set("X-Ruvicode-Version", "1.0")

	// Write the status explicitly so it is logged as 200 and the client starts
	// receiving the SSE stream.
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	usageCapture := &UsageCapture{}

	scanner := bufio.NewScanner(result.Stream)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB per line.
	flusher, _ := w.(http.Flusher)

	for scanner.Scan() {
		line := scanner.Bytes()

		if bytes.Contains(line, []byte(`"usage"`)) {
			usageCapture.ParseFromChunk(line)
		}

		// Strip the provider-reported cost object. The upstream cost is
		// internal margin data and must never reach the user, even when the
		// final streaming chunk echoes it.
		line = masking.StripCostField(line)

		_, _ = w.Write(line)
		_, _ = w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}

		if bytes.Equal(bytes.TrimSpace(line), []byte("data: [DONE]")) {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("stream read error", "error", err, "user_id", userID, "request_id", requestID)
	}
	_ = result.Stream.Close()

	actualCost := h.billing.CalculateActualCost(modelPrice, usageCapture.Usage)
	// Settle billing on a detached context: the request context is canceled as
	// soon as the client closes the stream after [DONE], and billing must
	// complete regardless (verified by E2E under concurrent load).
	h.billing.FinalizeDeduction(context.Background(), userID, estimatedCost, actualCost, preCheck, &billing.UsageInfo{
		Model:            modelPrice.Model,
		PromptTokens:     usageTokens(usageCapture.Usage).prompt,
		CompletionTokens: usageTokens(usageCapture.Usage).completion,
		APIKeyID:         keyData.KeyID,
		UpstreamCost:     usageCapture.UpstreamCost,
		RequestID:        requestID,
	})

	slog.Info("request_completed",
		"user_id", userID,
		"model", modelPrice.Model,
		"prompt_tokens", usageTokens(usageCapture.Usage).prompt,
		"completion_tokens", usageTokens(usageCapture.Usage).completion,
		"cost", actualCost,
		"estimated_cost", estimatedCost,
		"request_id", requestID,
	)
}

// handleNonStreamResponse writes the complete response body with usage data,
// sets the X-Cost header, and settles billing.
func (h *ChatHandler) handleNonStreamResponse(
	w http.ResponseWriter,
	r *http.Request,
	result *provider.ChatResult,
	userID string,
	keyData *store.APIKeyData,
	modelPrice *pricing.ModelPrice,
	estimatedCost float64,
	preCheck *billing.PreCheckResult,
) {
	requestID := middleware.RequestID(r.Context())

	if result.Body != nil {
		masking.CheckBodyForLeaks(result.Body, requestID)
	}

	actualCost := h.billing.CalculateActualCost(modelPrice, result.Usage)
	// Detached context for the same reason as the streaming path.
	h.billing.FinalizeDeduction(context.Background(), userID, estimatedCost, actualCost, preCheck, &billing.UsageInfo{
		Model:            modelPrice.Model,
		PromptTokens:     usageTokens(result.Usage).prompt,
		CompletionTokens: usageTokens(result.Usage).completion,
		APIKeyID:         keyData.KeyID,
		UpstreamCost:     result.UpstreamCost,
		RequestID:        requestID,
	})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Ruvicode-Request-ID", requestID)
	w.Header().Set("X-Ruvicode-Version", "1.0")
	w.Header().Set("X-Cost", masking.FormatCost(actualCost))
	w.WriteHeader(http.StatusOK)
	// Strip the provider-reported cost object from the body (upstream cost is
	// internal margin data); the user already receives the cost via X-Cost.
	_, _ = w.Write(masking.StripCostField(result.Body))
}

// messageCount returns the number of messages in the raw messages array.
func messageCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return 0
	}
	return len(msgs)
}

// parseMessages converts the raw messages array into provider.Message values.
func parseMessages(raw json.RawMessage) []provider.Message {
	if len(raw) == 0 {
		return nil
	}
	var msgs []provider.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil
	}
	return msgs
}

type tokenCounts struct {
	prompt     int
	completion int
}

// usageTokens safely extracts token counts from a possibly-nil Usage.
func usageTokens(u *provider.Usage) tokenCounts {
	if u == nil {
		return tokenCounts{}
	}
	return tokenCounts{prompt: u.PromptTokens, completion: u.CompletionTokens}
}
