package handler

import (
	"context"
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
// was called and what key data reached the context.
type recordingChat struct {
	called  bool
	keyData *store.APIKeyData
}

func (c *recordingChat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.called = true
	c.keyData = middleware.GetAPIKey(r.Context())
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

func activeTestKey(keyID, userID string) *store.APIKeyData {
	return &store.APIKeyData{
		KeyID:        keyID,
		UserID:       userID,
		IsActive:     true,
		RateLimitRPM: 60,
		Label:        "test",
	}
}
