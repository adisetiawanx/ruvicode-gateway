// Package middleware contains the HTTP middleware chain for the gateway:
// logging, API-key authentication, and per-key rate limiting.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/store"
)

type ctxKey string

const (
	// UserKey holds the authenticated user ID in the request context.
	UserKey ctxKey = "user"
	// APIKeyKey holds the authenticated API key data in the request context.
	APIKeyKey ctxKey = "api_key"
)

// apiKeyStore is the interface AuthMiddleware needs to resolve a key hash to
// key data when the Redis cache misses. The concrete PostgresStore satisfies
// it; the interface keeps the middleware testable without a live database.
type apiKeyStore interface {
	GetAPIKeyByHash(ctx context.Context, hash string) (*store.APIKeyData, error)
}

// AuthMiddleware validates rvcd_ Bearer API keys via the Redis cache with a
// Postgres fallback. It fails closed (401) on missing or invalid keys.
type AuthMiddleware struct {
	rdb *store.RedisStore
	pg  apiKeyStore
}

// NewAuth builds an AuthMiddleware.
func NewAuth(rdb *store.RedisStore, pg apiKeyStore) *AuthMiddleware {
	return &AuthMiddleware{rdb: rdb, pg: pg}
}

// Handler is the http.Handler wrapper.
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer rvcd_") {
			masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Missing or invalid API key")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		hash := SHA256Hex(token)
		ctx := r.Context()

		// 1. Redis cache (hot path).
		if cached, err := m.rdb.GetAPIKeyFromCache(ctx, hash); err == nil {
			if cached.IsActive {
				next.ServeHTTP(w, r.WithContext(withKey(ctx, cached)))
				return
			}
		}

		// 2. Postgres fallback.
		keyData, err := m.pg.GetAPIKeyByHash(ctx, hash)
		if err != nil || keyData == nil || !keyData.IsActive {
			masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Invalid API key")
			return
		}

		// 3. Cache for future requests (5-min TTL).
		_ = m.rdb.CacheAPIKey(ctx, hash, keyData)

		next.ServeHTTP(w, r.WithContext(withKey(ctx, keyData)))
	})
}

func withKey(ctx context.Context, keyData *store.APIKeyData) context.Context {
	ctx = context.WithValue(ctx, UserKey, keyData.UserID)
	ctx = context.WithValue(ctx, APIKeyKey, keyData)
	return ctx
}

// SHA256Hex returns the hex-encoded SHA-256 digest of a string.
func SHA256Hex(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

// GetUserID returns the authenticated user ID from the context.
func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(UserKey).(string); ok {
		return v
	}
	return ""
}

// GetAPIKey returns the authenticated key data from the context.
func GetAPIKey(ctx context.Context) *store.APIKeyData {
	if v, ok := ctx.Value(APIKeyKey).(*store.APIKeyData); ok {
		return v
	}
	return nil
}
