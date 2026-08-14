package store

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedis returns a RedisStore backed by an in-memory miniredis.
func newTestRedis(t *testing.T) *RedisStore {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &RedisStore{Client: client}
}

func TestCacheAPIKeyThenRead(t *testing.T) {
	rs := newTestRedis(t)
	ctx := context.Background()
	key := &APIKeyData{
		KeyID:        "key-1",
		UserID:       "user-1",
		IsActive:     true,
		RateLimitRPM: 60,
		Label:        "prod",
	}
	hash := "deadbeef"

	if err := rs.CacheAPIKey(ctx, hash, key); err != nil {
		t.Fatalf("cache: %v", err)
	}

	got, err := rs.GetAPIKeyFromCache(ctx, hash)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if got.KeyID != "key-1" || got.UserID != "user-1" || !got.IsActive || got.RateLimitRPM != 60 {
		t.Fatalf("unexpected cached data: %+v", got)
	}
}

// TestInvalidateAPIKeyCache is the revocation path: after DEL, a subsequent
// lookup is a cache miss, so the caller must fall back to Postgres.
func TestInvalidateAPIKeyCache(t *testing.T) {
	rs := newTestRedis(t)
	ctx := context.Background()
	hash := "cafebabe"

	if err := rs.CacheAPIKey(ctx, hash, &APIKeyData{KeyID: "k", UserID: "u"}); err != nil {
		t.Fatalf("cache: %v", err)
	}
	if _, err := rs.GetAPIKeyFromCache(ctx, hash); err != nil {
		t.Fatalf("expected cache hit before invalidation, got %v", err)
	}

	if err := rs.InvalidateAPIKeyCache(ctx, hash); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, err := rs.GetAPIKeyFromCache(ctx, hash); err == nil {
		t.Fatal("expected cache miss after invalidation")
	}
}

// TestGetAPIKeyFromCacheMiss confirms an unknown hash is a miss, not an error
// wrapped from a stale entry.
func TestGetAPIKeyFromCacheMiss(t *testing.T) {
	rs := newTestRedis(t)
	if _, err := rs.GetAPIKeyFromCache(context.Background(), "nope"); err == nil {
		t.Fatal("expected a cache miss error for an unknown hash")
	}
}