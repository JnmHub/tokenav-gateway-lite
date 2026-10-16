package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type keyBillingRateBucket struct {
	windowStart time.Time
	count       int
}

// KeyBillingRateLimiter 只保护倍率自省接口，不进入模型请求热路径。
type KeyBillingRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]keyBillingRateBucket
	lastGC  time.Time
}

func NewKeyBillingRateLimiter(limit int, window time.Duration) *KeyBillingRateLimiter {
	if limit <= 0 {
		limit = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	return &KeyBillingRateLimiter{limit: limit, window: window, buckets: make(map[string]keyBillingRateBucket)}
}

func (r *KeyBillingRateLimiter) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if r == nil {
			c.Next()
			return
		}
		identity := keyBillingRateIdentity(c)
		allowed, retryAfter := r.allow(identity, time.Now())
		if !allowed {
			c.Header("Retry-After", fmt.Sprintf("%d", maxRateLimitSecond(int(retryAfter.Seconds()))))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
				"type": "rate_limit_error", "code": "SUB2API_BILLING_RATE_LIMITED", "message": "Too many billing information requests",
			}})
			return
		}
		c.Next()
	}
}

func (r *KeyBillingRateLimiter) allow(identity string, now time.Time) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastGC.IsZero() || now.Sub(r.lastGC) >= r.window {
		cutoff := now.Add(-2 * r.window)
		for key, bucket := range r.buckets {
			if bucket.windowStart.Before(cutoff) {
				delete(r.buckets, key)
			}
		}
		r.lastGC = now
	}
	bucket := r.buckets[identity]
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) >= r.window {
		bucket = keyBillingRateBucket{windowStart: now}
	}
	if bucket.count >= r.limit {
		return false, bucket.windowStart.Add(r.window).Sub(now)
	}
	bucket.count++
	r.buckets[identity] = bucket
	return true, 0
}

func keyBillingRateIdentity(c *gin.Context) string {
	if key, ok := GetAPIKeyFromContext(c); ok && key != nil {
		if key.ID > 0 {
			return fmt.Sprintf("local:%d", key.ID)
		}
		if name := strings.TrimSpace(key.Name); name != "" {
			return "remote:" + name
		}
		return fmt.Sprintf("user:%d", key.UserID)
	}
	return "unknown"
}

func maxRateLimitSecond(value int) int {
	if value < 1 {
		return 1
	}
	return value
}
