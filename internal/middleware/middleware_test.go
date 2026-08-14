package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/ruvicode/gateway/internal/store"
)

// stubKeyStore lets tests supply GetAPIKeyByHash results without Postgres.
type stubKeyStore struct {
	data *store.APIKeyData
	err  error
}

func (s *stubKeyStore) GetAPIKeyByHash(_ context.Context, _ string) (*store.APIKeyData, error) {
	return s.data, s.err
}

var errNotFound = &storeErr{}

type storeErr struct{}

func (*storeErr) Error() string { return "api key not found" }

// redisStoreFor returns a store.RedisStore backed by a throwaway miniredis.
func redisStoreFor(t *testing.T) *store.RedisStore {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &store.RedisStore{Client: client}
}

// activeKey builds an active APIKeyData for a fake user/key.
func activeKey(keyID, userID string) *store.APIKeyData {
	return &store.APIKeyData{
		KeyID:        keyID,
		UserID:       userID,
		IsActive:     true,
		RateLimitRPM: 60,
		Label:        "test",
	}
}

func authedRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func okNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// --- Auth middleware ---

func TestAuthMissingHeaderFailsClosed(t *testing.T) {
	// Nil stores are safe here: a missing header must 401 before any store is
	// touched.
	m := NewAuth(nil, nil)
	rw := httptest.NewRecorder()

	m.Handler(okNext()).ServeHTTP(rw, authedRequest(""))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "authentication_error") {
		t.Fatalf("expected OpenAI error envelope, got %s", rw.Body.String())
	}
}

func TestAuthWrongTokenSchemeFailsClosed(t *testing.T) {
	m := NewAuth(nil, nil)
	rw := httptest.NewRecorder()

	// "Bearer sk-..." is a valid scheme but not a Ruvicode key.
	m.Handler(okNext()).ServeHTTP(rw, authedRequest("sk-1234567890abcdef"))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
}

func TestAuthCacheHitActivePasses(t *testing.T) {
	rdb := redisStoreFor(t)
	mr := rdb.Client
	keyData := activeKey("key-1", "user-1")
	token := "rvcd_" + strings.Repeat("a", 32)

	// Pre-populate the cache so the request is served from Redis without the
	// store being touched.
	raw, _ := json.Marshal(keyData)
	mr.Set(context.Background(), "apikey:"+SHA256Hex(token), raw, 0)

	m := NewAuth(rdb, nil)
	rw := httptest.NewRecorder()
	var gotKey *store.APIKeyData
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = GetAPIKey(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	m.Handler(next).ServeHTTP(rw, authedRequest(token))

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	if gotKey == nil || gotKey.KeyID != "key-1" || gotKey.UserID != "user-1" {
		t.Fatalf("expected key data in context, got %+v", gotKey)
	}
}

func TestAuthCacheHitRevokedRejected(t *testing.T) {
	rdb := redisStoreFor(t)
	// A revoked key is cached as inactive. The middleware must NOT trust the
	// cache and must fall through to the store, which returns not found.
	revoked := activeKey("key-2", "user-1")
	revoked.IsActive = false
	token := "rvcd_" + strings.Repeat("b", 32)
	raw, _ := json.Marshal(revoked)
	rdb.Client.Set(context.Background(), "apikey:"+SHA256Hex(token), raw, 0)

	pg := &stubKeyStore{err: errNotFound}
	m := NewAuth(rdb, pg)
	rw := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("next must not run for a revoked key")
	})

	m.Handler(next).ServeHTTP(rw, authedRequest(token))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked key, got %d", rw.Code)
	}
}

func TestAuthCacheMissFallsBackToStoreAndPopulates(t *testing.T) {
	rdb := redisStoreFor(t)
	keyData := activeKey("key-3", "user-2")
	token := "rvcd_" + strings.Repeat("c", 32)
	hash := SHA256Hex(token)

	pg := &stubKeyStore{data: keyData}
	m := NewAuth(rdb, pg)

	// Cache miss -> Postgres fallback -> 200 and cache populated.
	rw := httptest.NewRecorder()
	m.Handler(okNext()).ServeHTTP(rw, authedRequest(token))
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}

	if _, err := rdb.Client.Get(context.Background(), "apikey:"+hash).Result(); err != nil {
		t.Fatalf("expected cache populated after fallback: %v", err)
	}

	// The next request must hit the cache, so we make the store start failing
	// and confirm the cached key still passes.
	pg.data = nil
	pg.err = errNotFound
	rw = httptest.NewRecorder()
	m.Handler(okNext()).ServeHTTP(rw, authedRequest(token))
	if rw.Code != http.StatusOK {
		t.Fatalf("expected cached authed request to pass, got %d", rw.Code)
	}
}

func TestAuthCacheMissNotFoundRejected(t *testing.T) {
	rdb := redisStoreFor(t)
	token := "rvcd_" + strings.Repeat("d", 32)
	pg := &stubKeyStore{err: errNotFound}
	m := NewAuth(rdb, pg)
	rw := httptest.NewRecorder()

	m.Handler(okNext()).ServeHTTP(rw, authedRequest(token))

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
}

// --- Rate limit middleware ---

// keyedRequest injects key data + user id into the context so the rate limiter
// can run without the auth middleware.
func keyedRequest(keyID, userID string) *http.Request {
	r := authedRequest("")
	r = r.WithContext(withKey(r.Context(), &store.APIKeyData{
		KeyID:        keyID,
		UserID:       userID,
		RateLimitRPM: 2,
	}))
	return r
}

func TestRateLimitUnderLimitPasses(t *testing.T) {
	rdb := redisStoreFor(t)
	m := NewRateLimit(rdb)

	for i := 0; i < 2; i++ {
		rw := httptest.NewRecorder()
		m.Handler(okNext()).ServeHTTP(rw, keyedRequest("rl-1", "u1"))
		if rw.Code != http.StatusOK {
			t.Fatalf("request %d (under limit) expected 200, got %d", i+1, rw.Code)
		}
	}
}

func TestRateLimitOverLimit429(t *testing.T) {
	rdb := redisStoreFor(t)
	m := NewRateLimit(rdb)
	next := okNext()

	// Limit is 2; requests beyond the second in the window must be rejected
	// with the user-facing rate-limit headers. All four run inside the same
	// 60s window, so requests 3 and 4 are both 429.
	var limit, remaining string
	for i := 0; i < 4; i++ {
		rw := httptest.NewRecorder()
		m.Handler(next).ServeHTTP(rw, keyedRequest("rl-2", "u1"))
		limit = rw.Header().Get("X-RateLimit-Limit")
		remaining = rw.Header().Get("X-RateLimit-Remaining")
		if i < 2 {
			if rw.Code != http.StatusOK {
				t.Fatalf("request %d expected 200, got %d", i+1, rw.Code)
			}
		} else {
			if rw.Code != http.StatusTooManyRequests {
				t.Fatalf("request %d expected 429, got %d", i+1, rw.Code)
			}
			if rw.Header().Get("Retry-After") != "60" {
				t.Errorf("expected Retry-After 60, got %q", rw.Header().Get("Retry-After"))
			}
		}
	}
	if limit != "2" {
		t.Errorf("expected X-RateLimit-Limit 2, got %q", limit)
	}
	if remaining != "0" {
		t.Errorf("expected X-RateLimit-Remaining clamped to 0, got %q", remaining)
	}
}

func TestRateLimitIndependentKeys(t *testing.T) {
	rdb := redisStoreFor(t)
	m := NewRateLimit(rdb)

	// Exhaust key-a for user-1: third request is 429.
	for i := 0; i < 3; i++ {
		rw := httptest.NewRecorder()
		m.Handler(okNext()).ServeHTTP(rw, keyedRequest("key-a", "u1"))
		if i == 2 && rw.Code != http.StatusTooManyRequests {
			t.Fatalf("key-a request %d expected 429, got %d", i+1, rw.Code)
		}
	}

	// A different key for the same user is an independent counter.
	rw := httptest.NewRecorder()
	m.Handler(okNext()).ServeHTTP(rw, keyedRequest("key-b", "u1"))
	if rw.Code != http.StatusOK {
		t.Fatalf("independent key-b expected 200, got %d", rw.Code)
	}

	// A different user sharing the same key label is also independent.
	rw = httptest.NewRecorder()
	m.Handler(okNext()).ServeHTTP(rw, keyedRequest("key-a", "u2"))
	if rw.Code != http.StatusOK {
		t.Fatalf("user-2 key-a expected 200, got %d", rw.Code)
	}
}

func TestRateLimitWindowSlides(t *testing.T) {
	rdb := redisStoreFor(t)
	m := NewRateLimit(rdb)
	ctx := context.Background()

	// Seed two entries older than the 60s window; the limiter must prune them
	// so the current request still passes under a limit of 2.
	stale := time.Now().Unix() - 70
	redisKey := "ratelimit:u1:rl-slide"
	rdb.Client.ZAdd(ctx, redisKey, redis.Z{Score: float64(stale), Member: "old-1"})
	rdb.Client.ZAdd(ctx, redisKey, redis.Z{Score: float64(stale), Member: "old-2"})

	rw := httptest.NewRecorder()
	m.Handler(okNext()).ServeHTTP(rw, keyedRequest("rl-slide", "u1"))

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 after stale entries fall out of the window, got %d", rw.Code)
	}
	if count, _ := rdb.Client.ZCard(ctx, redisKey).Result(); count != 1 {
		t.Fatalf("expected 1 live member after sliding, got %d", count)
	}
}