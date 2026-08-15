package handler

import (
	"github.com/ruvicode/gateway/internal/catalog"

	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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

// maxRequestBodyBytes caps the request body to keep oversized payloads from
// buffering unbounded memory before the JSON decode (DoS guard).
const maxRequestBodyBytes = 10 << 20 // 10 MB

// Handle runs the full request lifecycle: parse, price, pre-check, route,
// proxy, and settle.
func (h *ChatHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	keyData := middleware.GetAPIKey(ctx)

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			masking.WriteOpenAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large")
			return
		}
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid JSON body")
		return
	}
	if req.Model == "" {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Model is required")
		return
	}
	// Curated catalog: the API only serves the allowlisted models.
	if !catalog.IsAllowed(req.Model) {
		masking.WriteOpenAIError(w, http.StatusNotFound, "invalid_request_error", "Model not found")
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
		h.billing.ReleaseHold(ctx, userID, keyData.KeyID, estimatedCost)
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
		h.billing.ReleaseHold(ctx, userID, keyData.KeyID, estimatedCost)
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

		// Skip SSE comments and keep-alive lines (they can name upstream
		// internals; EventSource ignores them anyway), but ALWAYS forward
		// blank lines: the empty line is the SSE event terminator, and a
		// strict parser (Vercel AI SDK, OpenCode, native EventSource)
		// buffers until it sees one. Without it every event glues into a
		// single blob that never dispatches.
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == ':' {
			continue
		}
		if len(trimmed) == 0 {
			_, _ = w.Write([]byte("\n"))
			if flusher != nil {
				flusher.Flush()
			}
			continue
		}

		if bytes.Contains(line, []byte(`"usage"`)) {
			usageCapture.ParseFromChunk(line)
		}

		// Mask the chunk before forwarding: strip the upstream cost and
		// provider fields, and rewrite the model to the requested id. The
		// upstream identity must never reach the user (ADR-022).
		line = masking.SanitizeResponseBody(line, modelPrice.Model)

		_, _ = w.Write(line)
		_, _ = w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}

		if bytes.Equal(trimmed, []byte("data: [DONE]")) {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("stream read error", "error", err, "user_id", userID, "request_id", requestID)
	}
	_ = result.Stream.Close()

	tokens := usageTokens(usageCapture.Usage)
	actualCost := h.billing.CalculateActualCost(modelPrice, usageCapture.Usage)
	refCost := h.billing.CalculateRefCost(modelPrice, usageCapture.Usage)
	// Settle billing on a detached context: the request context is canceled as
	// soon as the client closes the stream after [DONE], and billing must
	// complete regardless (verified by E2E under concurrent load).
	h.billing.FinalizeDeduction(context.Background(), userID, estimatedCost, actualCost, preCheck, &billing.UsageInfo{
		Model:            modelPrice.Model,
		PromptTokens:     tokens.prompt,
		CompletionTokens: tokens.completion,
		ReasoningTokens:  tokens.reasoning,
		APIKeyID:         keyData.KeyID,
		UpstreamCost:     usageCapture.UpstreamCost,
		RefCost:          refCost,
		RequestID:        requestID,
	})
	// Mark the key as used so the dashboard stops showing "Never".
	h.pg.UpdateLastUsed(context.Background(), keyData.KeyID)

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

	tokens := usageTokens(result.Usage)
	actualCost := h.billing.CalculateActualCost(modelPrice, result.Usage)
	refCost := h.billing.CalculateRefCost(modelPrice, result.Usage)
	// Detached context for the same reason as the streaming path.
	h.billing.FinalizeDeduction(context.Background(), userID, estimatedCost, actualCost, preCheck, &billing.UsageInfo{
		Model:            modelPrice.Model,
		PromptTokens:     tokens.prompt,
		CompletionTokens: tokens.completion,
		ReasoningTokens:  tokens.reasoning,
		APIKeyID:         keyData.KeyID,
		UpstreamCost:     result.UpstreamCost,
		RefCost:          refCost,
		RequestID:        requestID,
	})
	// Mark the key as used so the dashboard stops showing "Never".
	h.pg.UpdateLastUsed(context.Background(), keyData.KeyID)

	// Headers are built from scratch (provider headers are never copied to the
	// client), and the masking layer re-asserts the Ruvicode-branded headers
	// and the user's rate-limit values. SanitizeHeaders also strips any stray
	// upstream header that a future refactor might otherwise propagate.
	w.Header().Set("Content-Type", "application/json")
	limitVal, _ := strconv.Atoi(w.Header().Get("X-RateLimit-Limit"))
	remainingVal, _ := strconv.Atoi(w.Header().Get("X-RateLimit-Remaining"))
	masking.SanitizeHeaders(w.Header(), requestID, limitVal, remainingVal)
	w.Header().Set("X-Cost", masking.FormatCost(actualCost))
	w.WriteHeader(http.StatusOK)
	// Mask the body before writing: strip the upstream cost and provider
	// fields and rewrite the model to the requested id (the user already
	// receives the cost via X-Cost).
	_, _ = w.Write(masking.SanitizeResponseBody(result.Body, modelPrice.Model))
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
// Tool-call fields ride along untouched; assistant messages carrying tool
// calls with a null content (the exact shape the OpenAI SDK emits after a
// tool call) are normalized to an empty string because some upstreams
// reject that combination outright with a 400.
func parseMessages(raw json.RawMessage) []provider.Message {
	if len(raw) == 0 {
		return nil
	}
	var msgs []provider.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil
	}
	for i := range msgs {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			if c := msgs[i].Content; len(c) == 0 || bytes.Equal(bytes.TrimSpace(c), []byte("null")) {
				msgs[i].Content = json.RawMessage(`""`)
			}
		}
	}
	return msgs
}

type tokenCounts struct {
	prompt     int
	completion int
	reasoning  int
}

// usageTokens safely extracts token counts from a possibly-nil Usage.
func usageTokens(u *provider.Usage) tokenCounts {
	if u == nil {
		return tokenCounts{}
	}
	return tokenCounts{prompt: u.PromptTokens, completion: u.CompletionTokens, reasoning: u.ReasoningTokens}
}
