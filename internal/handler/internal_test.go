package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ruvicode/gateway/internal/middleware"
	"github.com/ruvicode/gateway/internal/store"
)

type stubKeyStore struct {
	data *store.APIKeyData
	err  error
}

func (s *stubKeyStore) GetAPIKeyByIDAndUser(_ context.Context, userID, keyID string) (*store.APIKeyData, error) {
	return s.data, s.err
}

var errNotFound = &storeErr{}

type storeErr struct{}

func (*storeErr) Error() string { return "api key not found" }

// recordingChat is a stand-in for the chat pipeline; it records whether it
// was called, what key data reached the context, and what body it received.
type recordingChat struct {
	called  bool
	keyData *store.APIKeyData
	body    string
}

func (c *recordingChat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.called = true
	c.keyData = middleware.GetAPIKey(r.Context())
	b, _ := io.ReadAll(r.Body)
	c.body = string(b)
	w.WriteHeader(http.StatusOK)
}

func internalRequest(token string, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/internal/playground/chat", strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Internal-Token", token)
	}
	return r
}

const validInternalBody = `{"user_id":"user-1","key_id":"key-1","model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`

func TestInternalChatRejectsMissingToken(t *testing.T) {
	chat := &recordingChat{}
	h := NewInternalChatHandler(&stubKeyStore{}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("", validInternalBody))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
	if chat.called {
		t.Fatal("chat pipeline must not run without a valid token")
	}
}

func TestInternalChatRejectsWrongToken(t *testing.T) {
	chat := &recordingChat{}
	h := NewInternalChatHandler(&stubKeyStore{}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("wrong", validInternalBody))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
	if chat.called {
		t.Fatal("chat pipeline must not run with a wrong token")
	}
}

func TestInternalChatRejectsBadBody(t *testing.T) {
	chat := &recordingChat{}
	h := NewInternalChatHandler(&stubKeyStore{}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("sekrit", `{"user_id":"user-1"}`)) // missing key_id

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
	if chat.called {
		t.Fatal("chat pipeline must not run for an invalid body")
	}
}

func TestInternalChatRejectsMissingKey(t *testing.T) {
	chat := &recordingChat{}
	h := NewInternalChatHandler(&stubKeyStore{err: errNotFound}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("sekrit", validInternalBody))

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "Create one in the dashboard") {
		t.Fatalf("expected create-key guidance, got %s", rw.Body.String())
	}
	if chat.called {
		t.Fatal("chat pipeline must not run without a resolvable key")
	}
}

func TestInternalChatRejectsInactiveKey(t *testing.T) {
	chat := &recordingChat{}
	key := activeTestKey("key-1", "user-1")
	key.IsActive = false
	h := NewInternalChatHandler(&stubKeyStore{data: key}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("sekrit", validInternalBody))

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for inactive key, got %d", rw.Code)
	}
	if chat.called {
		t.Fatal("chat pipeline must not run for an inactive key")
	}
}

func TestInternalChatDelegatesWithKeyInContext(t *testing.T) {
	chat := &recordingChat{}
	key := activeTestKey("key-1", "user-1")
	h := NewInternalChatHandler(&stubKeyStore{data: key}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("sekrit", validInternalBody))

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	if !chat.called {
		t.Fatal("chat pipeline must run for a valid internal request")
	}
	if chat.keyData == nil || chat.keyData.KeyID != "key-1" || chat.keyData.UserID != "user-1" {
		t.Fatalf("expected key data in context, got %+v", chat.keyData)
	}
}

func TestInternalChatInjectsIdentityPrompt(t *testing.T) {
	chat := &recordingChat{}
	key := activeTestKey("key-1", "user-1")
	h := NewInternalChatHandler(&stubKeyStore{data: key}, chat, "sekrit")
	rw := httptest.NewRecorder()

	h.Handle(rw, internalRequest("sekrit", validInternalBody))

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}

	var body struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(chat.body), &body); err != nil {
		t.Fatalf("chat body is not valid json: %v", err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("expected identity system message prepended (2 messages), got %d", len(body.Messages))
	}
	first := body.Messages[0]
	if first["role"] != "system" {
		t.Fatalf("expected first message role system, got %v", first["role"])
	}
	content, _ := first["content"].(string)
	if !strings.Contains(content, "You are DeepSeek V4 Flash") {
		t.Fatalf("expected identity prompt naming DeepSeek V4 Flash, got %q", content)
	}
	if !strings.Contains(content, "API gateway") {
		t.Fatalf("expected gateway context in identity prompt, got %q", content)
	}
	// The user's own messages stay intact, in order, after the prompt.
	second := body.Messages[1]
	if second["role"] != "user" || second["content"] != "hi" {
		t.Fatalf("expected original user message preserved, got %+v", second)
	}
}

func TestInternalChatRejectsUnknownModel(t *testing.T) {
	chat := &recordingChat{}
	key := activeTestKey("key-1", "user-1")
	h := NewInternalChatHandler(&stubKeyStore{data: key}, chat, "sekrit")
	rw := httptest.NewRecorder()

	body := `{"user_id":"user-1","key_id":"key-1","model":"not-a-real-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	h.Handle(rw, internalRequest("sekrit", body))

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a model outside the catalog, got %d", rw.Code)
	}
	if chat.called {
		t.Fatal("chat pipeline must not run for an unknown model")
	}
}

func TestInternalChatKeepsUserSystemMessage(t *testing.T) {
	chat := &recordingChat{}
	key := activeTestKey("key-1", "user-1")
	h := NewInternalChatHandler(&stubKeyStore{data: key}, chat, "sekrit")
	rw := httptest.NewRecorder()

	body := `{"user_id":"user-1","key_id":"key-1","model":"glm-5.2","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hi"}],"stream":true}`
	h.Handle(rw, internalRequest("sekrit", body))

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var parsed struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(chat.body), &parsed); err != nil {
		t.Fatalf("chat body is not valid json: %v", err)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("expected 3 messages (identity + user system + user), got %d", len(parsed.Messages))
	}
	if parsed.Messages[1]["content"] != "be terse" {
		t.Fatalf("expected user system message kept after identity prompt, got %+v", parsed.Messages[1])
	}
	first := parsed.Messages[0]
	content, _ := first["content"].(string)
	if !strings.Contains(content, "You are GLM-5.2") {
		t.Fatalf("expected identity prompt naming GLM-5.2, got %q", content)
	}
}

func activeTestKey(keyID, userID string) *store.APIKeyData {
	return &store.APIKeyData{
		KeyID:        keyID,
		UserID:       userID,
		IsActive:     true,
		RateLimitRPM: 60,
		Label:        "test",
	}
}
