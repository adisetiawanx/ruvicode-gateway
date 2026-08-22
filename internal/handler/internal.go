package handler

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ruvicode/gateway/internal/catalog"
	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/middleware"
	"github.com/ruvicode/gateway/internal/store"
)

// internalPlaygroundRequest is what the dashboard sends to the internal
// playground endpoint. It names the signed-in user's key id instead of the
// full key, which the web server never stores or handles.
type internalPlaygroundRequest struct {
	UserID      string          `json:"user_id"`
	KeyID       string          `json:"key_id"`
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   *int            `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
}

// keyStore is what InternalChatHandler needs to resolve a user's key by id.
// The concrete PostgresStore satisfies it; the interface keeps the handler
// testable without a live database.
type keyStore interface {
	GetAPIKeyByIDAndUser(ctx context.Context, userID, keyID string) (*store.APIKeyData, error)
}

// InternalChatHandler exposes the normal chat pipeline to the dashboard
// without a user API key in the browser or the web server. The web server
// authenticates with a shared token (X-Internal-Token) and names the user's
// key id; the gateway resolves the key, applies its rate and spend limits,
// and bills the user's wallet exactly as a normal request would.
//
// The handler is wired with the rate-limit middleware already wrapped around
// the chat handler, so the key's per-request limits apply here too.
type InternalChatHandler struct {
	keys  keyStore
	chat  http.Handler
	token string
}

// NewInternalChatHandler builds the internal handler. chat should be the
// rate-limited chat pipeline (rateLimit middleware around ChatHandler.Handle).
func NewInternalChatHandler(keys keyStore, chat http.Handler, token string) *InternalChatHandler {
	return &InternalChatHandler{keys: keys, chat: chat, token: token}
}

// playgroundIdentityPrompt states the model's actual identity so it does not
// answer "which model am I" from stale training data. It reads as natural
// facts, no product name and no prohibition-style wording, and matches the
// prompt the web routes previously injected.
func playgroundIdentityPrompt(displayName string) string {
	return fmt.Sprintf(
		"You are %s, running behind an API gateway. Your knowledge of your own version may be out of date. When the user asks which model or version they are talking to, you are %s. Keep the same tone and personality you normally have, and answer other questions as yourself.",
		displayName, displayName,
	)
}

// injectIdentityPrompt prepends the identity system message to the raw
// messages JSON. A user-supplied system message, if any, is kept after it.
func injectIdentityPrompt(rawMessages json.RawMessage, displayName string) (json.RawMessage, error) {
	var messages []map[string]any
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return nil, err
	}
	prepend := []map[string]any{
		{"role": "system", "content": playgroundIdentityPrompt(displayName)},
	}
	return json.Marshal(append(prepend, messages...))
}

// Handle validates the shared token, resolves the user's key, injects it into
// the request context, and delegates to the normal chat pipeline.
func (h *InternalChatHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// 1. Shared-token authentication (constant time).
	got := []byte(r.Header.Get("X-Internal-Token"))
	want := []byte(h.token)
	if len(want) == 0 || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Unauthorized")
		return
	}

	// 2. Parse the internal request body.
	var req internalPlaygroundRequest
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request body")
		return
	}
	if err := json.Unmarshal(rawBody, &req); err != nil ||
		req.UserID == "" || req.KeyID == "" || req.Model == "" || len(req.Messages) == 0 {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request parameters")
		return
	}

	// 3. Resolve the user's key (id scoped to the user, never the full key).
	keyData, err := h.keys.GetAPIKeyByIDAndUser(r.Context(), req.UserID, req.KeyID)
	if err != nil || keyData == nil || !keyData.IsActive {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error",
			"No active API key. Create one in the dashboard first.")
		return
	}

	// 3b. Identity context: models name themselves from stale training data,
	// so state the actual model here, server-side, where the browser payload
	// never carries it. Resolved from the curated catalog's display name.
	meta, ok := catalog.Meta(req.Model)
	if !ok {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request parameters")
		return
	}
	messages, err := injectIdentityPrompt(req.Messages, meta.DisplayName)
	if err != nil {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request parameters")
		return
	}

	// 4. Inject the key into the context, strip the internal fields from the
	// body, and run the normal pipeline (rate limit, billing, provider,
	// masking, streaming).
	ctx := context.WithValue(r.Context(), middleware.UserKey, keyData.UserID)
	ctx = context.WithValue(ctx, middleware.APIKeyKey, keyData)
	r = r.WithContext(ctx)

	chatBody := map[string]any{
		"model":       req.Model,
		"messages":    messages,
		"stream":      req.Stream,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
	}
	if req.MaxTokens == nil {
		delete(chatBody, "max_tokens")
	}
	if req.Temperature == nil {
		delete(chatBody, "temperature")
	}
	rebuilt, err := json.Marshal(chatBody)
	if err != nil {
		masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "An unexpected error occurred")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(rebuilt))
	r.ContentLength = int64(len(rebuilt))

	h.chat.ServeHTTP(w, r)
}
