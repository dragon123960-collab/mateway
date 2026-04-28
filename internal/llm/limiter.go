package llm

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ModelLimiter applies a conservative local request budget and cooldown window.
// It is intentionally simple: no internal queue, no background consumers.
type ModelLimiter struct {
	mu               sync.Mutex
	limiter          *rate.Limiter
	cooldownBase     time.Duration
	cooldownCurrent  time.Duration
	cooldownMax      time.Duration
	lockedUntil      time.Time
	consecutive429   int
	consecutiveOK    int
	successCount     int64
	rateLimitedCount int64
}

type LimiterHub struct {
	mu              sync.Mutex
	rpm             int
	cooldownSeconds int
	limiters        map[string]*ModelLimiter
}

func NewModelLimiter(rpm int, cooldownSeconds int) *ModelLimiter {
	if rpm <= 0 {
		rpm = 12
	}
	if cooldownSeconds <= 0 {
		cooldownSeconds = 60
	}
	perSecond := rate.Limit(float64(rpm) / 60.0)
	if perSecond <= 0 {
		perSecond = rate.Limit(1)
	}
	burst := max(1, min(rpm, 4))
	base := time.Duration(cooldownSeconds) * time.Second
	return &ModelLimiter{
		limiter:         rate.NewLimiter(perSecond, burst),
		cooldownBase:    base,
		cooldownCurrent: base,
		cooldownMax:     5 * time.Minute,
	}
}

func NewLimiterHub(rpm int, cooldownSeconds int) *LimiterHub {
	return &LimiterHub{
		rpm:             rpm,
		cooldownSeconds: cooldownSeconds,
		limiters:        make(map[string]*ModelLimiter),
	}
}

func (h *LimiterHub) For(provider, model string) *ModelLimiter {
	if h == nil {
		return nil
	}
	key := limiterKey(provider, model)
	h.mu.Lock()
	defer h.mu.Unlock()
	if item, ok := h.limiters[key]; ok {
		return item
	}
	item := NewModelLimiter(h.rpm, h.cooldownSeconds)
	h.limiters[key] = item
	return item
}

func (h *LimiterHub) Describe(provider, model string) string {
	if h == nil {
		return ""
	}
	return h.For(provider, model).DescribeState()
}

func limiterKey(provider, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "default"
	}
	return provider + "::" + model
}

func (l *ModelLimiter) Wait(ctx context.Context) error {
	for {
		waitFor := l.remainingCooldown()
		if waitFor > 0 {
			timer := time.NewTimer(waitFor)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if l.limiter == nil {
			return nil
		}
		return l.limiter.Wait(ctx)
	}
}

func (l *ModelLimiter) MarkRateLimited() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rateLimitedCount++
	l.consecutive429++
	l.consecutiveOK = 0
	multiplier := math.Pow(2, float64(max(0, l.consecutive429-1)))
	cooldown := time.Duration(float64(l.cooldownBase) * multiplier)
	if cooldown > l.cooldownMax {
		cooldown = l.cooldownMax
	}
	l.cooldownCurrent = cooldown
	l.lockedUntil = time.Now().Add(cooldown)
}

func (l *ModelLimiter) Success() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.successCount++
	l.consecutiveOK++
	if l.consecutiveOK < 3 {
		return
	}
	l.consecutive429 = 0
	l.cooldownCurrent = l.cooldownBase
	l.lockedUntil = time.Time{}
}

func (l *ModelLimiter) IsLocked() bool {
	return l.remainingCooldown() > 0
}

func (l *ModelLimiter) RemainingCooldown() time.Duration {
	return l.remainingCooldown()
}

func (l *ModelLimiter) Stats() (successCount, rateLimitedCount int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.successCount, l.rateLimitedCount
}

func (l *ModelLimiter) DescribeState() string {
	remaining := l.remainingCooldown()
	if remaining > 0 {
		return fmt.Sprintf("cooling_down remaining=%s", remaining.Round(time.Second))
	}
	successCount, rateLimitedCount := l.Stats()
	return fmt.Sprintf("ready success=%d rate_limited=%d", successCount, rateLimitedCount)
}

func (l *ModelLimiter) remainingCooldown() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lockedUntil.IsZero() {
		return 0
	}
	remaining := time.Until(l.lockedUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
