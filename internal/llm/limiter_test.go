package llm

import (
	"context"
	"testing"
	"time"
)

func TestModelLimiterCooldownAndSuccessReset(t *testing.T) {
	limiter := NewModelLimiter(60, 1)
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("unexpected wait error: %v", err)
	}
	limiter.MarkRateLimited()
	if !limiter.IsLocked() {
		t.Fatal("expected limiter to enter cooldown after rate limit")
	}
	if limiter.RemainingCooldown() <= 0 {
		t.Fatal("expected positive cooldown")
	}
	limiter.Success()
	limiter.Success()
	if !limiter.IsLocked() {
		t.Fatal("expected cooldown to remain before enough successes")
	}
	limiter.Success()
	if limiter.IsLocked() {
		t.Fatal("expected cooldown to clear after consecutive successes")
	}
	success, rateLimited := limiter.Stats()
	if success != 3 {
		t.Fatalf("unexpected success count: %d", success)
	}
	if rateLimited != 1 {
		t.Fatalf("unexpected rate limited count: %d", rateLimited)
	}
}

func TestModelLimiterWaitHonorsContextDuringCooldown(t *testing.T) {
	limiter := NewModelLimiter(60, 10)
	limiter.MarkRateLimited()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := limiter.Wait(ctx)
	if err == nil {
		t.Fatal("expected wait to stop on context deadline")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("wait ignored context cancellation: %s", time.Since(start))
	}
}
