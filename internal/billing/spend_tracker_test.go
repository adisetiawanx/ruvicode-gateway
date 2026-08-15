package billing

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/ruvicode/gateway/internal/store"
)

// newTestEngine returns an Engine backed by a throwaway miniredis and a nil
// Postgres pool (tests here only exercise the Redis-side tracker logic; the
// Postgres fallback paths need a live DB and are covered by E2E).
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &Engine{
		pg:  &store.PostgresStore{},
		rdb: &store.RedisStore{Client: client},
	}
}

func strPtr(s string) *string { return &s }

// TestSpendTrackersWrittenOnPreCheck verifies both the daily AND monthly
// trackers are populated by PreCheck (the monthly field previously had no
// writer, so a monthly-limited key always fell back to Postgres).
func TestSpendTrackersWrittenOnPreCheck(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	// Seed the balance cache so PreCheck passes without Postgres, and warm
	// the spend trackers at zero so the limit reads stay on the Redis path.
	e.rdb.Client.HSet(ctx, "balance:user1", "balance", 10.0, "held", 0.0)
	e.rdb.Client.Set(ctx, spendKeyDaily("user1", "key1"), 0.0, 0)
	e.rdb.Client.Set(ctx, spendKeyMonthly("user1", "key1"), 0.0, 0)

	keyData := &store.APIKeyData{
		KeyID:             "key1",
		UserID:            "user1",
		IsActive:          true,
		RateLimitRPM:      60,
		SpendLimitMonthly: strPtr("5.0"),
	}

	if _, err := e.PreCheck(ctx, "user1", 1.0, keyData); err != nil {
		t.Fatalf("PreCheck failed: %v", err)
	}

	daily, err := e.rdb.Client.Get(ctx, spendKeyDaily("user1", "key1")).Float64()
	if err != nil || daily != 1.0 {
		t.Fatalf("daily tracker = %f (err %v), want 1.0", daily, err)
	}
	monthly, err := e.rdb.Client.Get(ctx, spendKeyMonthly("user1", "key1")).Float64()
	if err != nil || monthly != 1.0 {
		t.Fatalf("monthly tracker = %f (err %v), want 1.0", monthly, err)
	}
}

// TestReleaseHoldRollsBackTrackers verifies a failed request no longer
// consumes spend limits (previously only the hold was released).
func TestReleaseHoldRollsBackTrackers(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	e.rdb.Client.HSet(ctx, "balance:user1", "balance", 10.0, "held", 0.0)
	e.rdb.Client.IncrByFloat(ctx, spendKeyDaily("user1", "key1"), 1.5)
	e.rdb.Client.IncrByFloat(ctx, spendKeyMonthly("user1", "key1"), 1.5)
	e.rdb.Client.HIncrByFloat(ctx, "balance:user1", "held", 1.5)

	e.ReleaseHold(ctx, "user1", "key1", 1.5)

	daily, _ := e.rdb.Client.Get(ctx, spendKeyDaily("user1", "key1")).Float64()
	monthly, _ := e.rdb.Client.Get(ctx, spendKeyMonthly("user1", "key1")).Float64()
	held, _ := e.rdb.Client.HGet(ctx, "balance:user1", "held").Float64()
	if daily != 0 {
		t.Fatalf("daily tracker = %f after release, want 0", daily)
	}
	if monthly != 0 {
		t.Fatalf("monthly tracker = %f after release, want 0", monthly)
	}
	if held != 0 {
		t.Fatalf("held = %f after release, want 0", held)
	}
}

// TestSpendKeyCalendarRotation verifies the trackers are scoped to the UTC
// calendar day/month, so the window resets at the boundary instead of living
// on a sliding 24h TTL that never expires for active keys.
func TestSpendKeyCalendarRotation(t *testing.T) {
	day := spendKeyDaily("u", "k")
	month := spendKeyMonthly("u", "k")

	wantDayPrefix := "spend:u:k:d" + time.Now().UTC().Format("20060102")
	wantMonthPrefix := "spend:u:k:m" + time.Now().UTC().Format("200601")
	if day != wantDayPrefix {
		t.Fatalf("daily key = %q, want prefix %q", day, wantDayPrefix)
	}
	if month != wantMonthPrefix {
		t.Fatalf("monthly key = %q, want prefix %q", month, wantMonthPrefix)
	}
}

// TestSpendLimitRejectsWhenExceeded verifies the monthly limit check reads
// the populated tracker (Redis path, no Postgres needed).
func TestSpendLimitRejectsWhenExceeded(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	e.rdb.Client.HSet(ctx, "balance:user1", "balance", 100.0, "held", 0.0)
	// Yesterday's spend lives under a different key and must not count today.
	e.rdb.Client.Set(ctx, "spend:user1:key1:d"+time.Now().UTC().AddDate(0, 0, -1).Format("20060102"), 99.0, 0)
	e.rdb.Client.Set(ctx, spendKeyMonthly("user1", "key1"), 4.5, 0)

	keyData := &store.APIKeyData{
		KeyID:             "key1",
		UserID:            "user1",
		IsActive:          true,
		RateLimitRPM:      60,
		SpendLimitMonthly: strPtr("5.0"),
	}

	if _, err := e.PreCheck(ctx, "user1", 1.0, keyData); err == nil {
		t.Fatal("PreCheck should reject: monthly spend 4.5 + 1.0 > limit 5.0")
	}
}
