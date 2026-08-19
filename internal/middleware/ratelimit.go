package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/store"
)

// reqCounter gives each request a unique ZSET member so requests within the
// same second still accumulate in the sliding window.
var reqCounter atomic.Uint64

// RateLimitMiddleware enforces a per-user+per-key sliding-window rate limit
// using a Redis ZSET of request timestamps.
type RateLimitMiddleware struct {
	rdb *store.RedisStore
}

// NewRateLimit builds a RateLimitMiddleware.
func NewRateLimit(rdb *store.RedisStore) *RateLimitMiddleware {
	return &RateLimitMiddleware{rdb: rdb}
}

// Handler is the http.Handler wrapper.
func (m *RateLimitMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyData := GetAPIKey(r.Context())
		if keyData == nil {
			next.ServeHTTP(w, r)
			return
		}
		userID := GetUserID(r.Context())

		redisKey := "ratelimit:" + userID + ":" + keyData.KeyID
		ctx := r.Context()
		now := time.Now().Unix()

		// Atomic sliding window: prune expired, add current (unique member),
		// count, set TTL.
		pipe := m.rdb.Client.TxPipeline()
		pipe.ZRemRangeByScore(ctx, redisKey, "0", strconv.FormatInt(now-60, 10))
		pipe.ZAdd(ctx, redisKey, redis.Z{Score: float64(now), Member: fmt.Sprintf("%d-%d", now, reqCounter.Add(1))})
		countCmd := pipe.ZCard(ctx, redisKey)
		pipe.Expire(ctx, redisKey, 60*time.Second)
		if _, err := pipe.Exec(ctx); err != nil {
			// Fail OPEN but log: a slow/noisy Redis must not stonewall users.
			slog.Error("rate limit check failed", "error", err, "user_id", userID)
			next.ServeHTTP(w, r)
			return
		}

		count := countCmd.Val()
		limit := keyData.RateLimitRPM
		// Hard cap: the product ceiling is 3000 RPM per key. This guards
		// against stale or manually-edited rows above the configured max.
		if limit > 3000 {
			limit = 3000
		}
		remaining := limit - int(count)
		if remaining < 0 {
			remaining = 0
		}

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		if count > int64(limit) {
			w.Header().Set("Retry-After", "60")
			masking.WriteOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error",
				"Rate limit exceeded. Retry after 60 seconds.")
			return
		}

		next.ServeHTTP(w, r)
	})
}
