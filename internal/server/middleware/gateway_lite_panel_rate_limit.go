package middleware

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GatewayLitePanelRateLimiter 只保护 gateway-lite 身份认证后的后台 API。
// 进程内计数避免 Redis 故障反向锁死后台；运行配置来自 SettingService
// 的内存快照，不会把 DB 读取带入每个后台请求。
type GatewayLitePanelRateLimiter struct {
	mu             sync.Mutex
	settings       *service.SettingService
	fallback       service.PanelRateLimitSettings
	buckets        map[string]*gatewayLitePanelRateBucket
	operations     uint64
	now            func() time.Time
	checkedTotal   atomic.Uint64
	rejectedTotal  atomic.Uint64
	disabledBypass atomic.Uint64
	recoveryBypass atomic.Uint64
}

type gatewayLitePanelRateBucket struct {
	global   []time.Time
	heavy    []time.Time
	lastSeen time.Time
}

type GatewayLitePanelRateLimitRuntimeStats struct {
	CheckedTotal   uint64 `json:"checked_total"`
	RejectedTotal  uint64 `json:"rejected_total"`
	DisabledBypass uint64 `json:"disabled_bypass_total"`
	RecoveryBypass uint64 `json:"recovery_bypass_total"`
	ActiveBuckets  int    `json:"active_buckets"`
}

// NewGatewayLitePanelRateLimiter 保留旧构造签名，用于存量测试和无 SettingService 的极简运行。
func NewGatewayLitePanelRateLimiter(globalLimit, heavyLimit int, window time.Duration) *GatewayLitePanelRateLimiter {
	windowSeconds := int(window / time.Second)
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	return newGatewayLitePanelRateLimiter(nil, service.PanelRateLimitSettings{
		Enabled: true, Preset: service.PanelRateLimitPresetCustom,
		UserRPM: globalLimit, HeavyRPM: heavyLimit, WindowSeconds: windowSeconds,
	})
}

// NewGatewayLitePanelRateLimiterWithSettings 是生产构造器；配置修改后下一个请求立即生效。
func NewGatewayLitePanelRateLimiterWithSettings(settings *service.SettingService) *GatewayLitePanelRateLimiter {
	return newGatewayLitePanelRateLimiter(settings, service.DefaultPanelRateLimitSettings())
}

func newGatewayLitePanelRateLimiter(settings *service.SettingService, fallback service.PanelRateLimitSettings) *GatewayLitePanelRateLimiter {
	if normalized, err := service.NormalizePanelRateLimitSettings(fallback); err == nil {
		fallback = normalized
	} else {
		fallback = service.DefaultPanelRateLimitSettings()
	}
	limiter := &GatewayLitePanelRateLimiter{
		settings: settings, fallback: fallback,
		buckets: make(map[string]*gatewayLitePanelRateBucket), now: time.Now,
	}
	activeGatewayLitePanelRateLimiter.Store(limiter)
	return limiter
}

func (r *GatewayLitePanelRateLimiter) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if r == nil {
			c.Next()
			return
		}
		// 配置查看、关闭和恢复默认必须永远可达。否则管理员一旦选错严格
		// 档位，恰好命中 429 后连快速回滚接口本身也会被限流。
		if gatewayLitePanelRecoveryPath(c.Request.URL.Path) {
			r.recoveryBypass.Add(1)
			c.Next()
			return
		}
		settings := r.effectiveSettings(c.Request.Context())
		if !settings.Enabled {
			r.disabledBypass.Add(1)
			c.Next()
			return
		}
		r.checkedTotal.Add(1)
		now := r.now()
		heavy := gatewayLitePanelHeavyPath(c.Request.URL.Path)
		allowed, retryAfter := r.allow(gatewayLitePanelIdentity(c), heavy, now, settings)
		if !allowed {
			r.rejectedTotal.Add(1)
			ingress.Record(ingress.RejectAdminRateLimit)
			seconds := int(math.Ceil(retryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			c.Header("Retry-After", strconv.Itoa(seconds))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code": "RATE_LIMITED", "message": "管理请求过于频繁，请稍后重试",
			})
			return
		}
		c.Next()
	}
}

func (r *GatewayLitePanelRateLimiter) effectiveSettings(ctx context.Context) service.PanelRateLimitSettings {
	if r != nil && r.settings != nil {
		return r.settings.GetPanelRateLimitSettingsCached(ctx)
	}
	if r == nil {
		return service.DefaultPanelRateLimitSettings()
	}
	return r.fallback
}

func (r *GatewayLitePanelRateLimiter) allow(identity string, heavy bool, now time.Time, settings service.PanelRateLimitSettings) (bool, time.Duration) {
	window := time.Duration(settings.WindowSeconds) * time.Second
	if window <= 0 {
		window = time.Minute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-window)
	bucket := r.buckets[identity]
	if bucket == nil {
		bucket = &gatewayLitePanelRateBucket{}
		r.buckets[identity] = bucket
	}
	bucket.global = trimGatewayLitePanelWindow(bucket.global, cutoff)
	bucket.heavy = trimGatewayLitePanelWindow(bucket.heavy, cutoff)
	bucket.lastSeen = now
	if len(bucket.global) >= settings.UserRPM {
		return false, window - now.Sub(bucket.global[0])
	}
	if heavy && len(bucket.heavy) >= settings.HeavyRPM {
		return false, window - now.Sub(bucket.heavy[0])
	}
	bucket.global = append(bucket.global, now)
	if heavy {
		bucket.heavy = append(bucket.heavy, now)
	}
	r.operations++
	if r.operations%256 == 0 {
		staleBefore := now.Add(-2 * window)
		for key, candidate := range r.buckets {
			if candidate.lastSeen.Before(staleBefore) {
				delete(r.buckets, key)
			}
		}
	}
	return true, 0
}

func (r *GatewayLitePanelRateLimiter) Stats() GatewayLitePanelRateLimitRuntimeStats {
	if r == nil {
		return GatewayLitePanelRateLimitRuntimeStats{}
	}
	r.mu.Lock()
	activeBuckets := len(r.buckets)
	r.mu.Unlock()
	return GatewayLitePanelRateLimitRuntimeStats{
		CheckedTotal: r.checkedTotal.Load(), RejectedTotal: r.rejectedTotal.Load(),
		DisabledBypass: r.disabledBypass.Load(), RecoveryBypass: r.recoveryBypass.Load(), ActiveBuckets: activeBuckets,
	}
}

func (r *GatewayLitePanelRateLimiter) RuntimeMetrics(context.Context) map[string]any {
	stats := r.Stats()
	settings := r.effectiveSettings(context.Background())
	return map[string]any{
		"panel_rate_limit_enabled":               settings.Enabled,
		"panel_rate_limit_preset":                settings.Preset,
		"panel_rate_limit_user_rpm":              settings.UserRPM,
		"panel_rate_limit_heavy_rpm":             settings.HeavyRPM,
		"panel_rate_limit_window_seconds":        settings.WindowSeconds,
		"panel_rate_limit_checked_total":         stats.CheckedTotal,
		"panel_rate_limit_rejected_total":        stats.RejectedTotal,
		"panel_rate_limit_disabled_bypass_total": stats.DisabledBypass,
		"panel_rate_limit_recovery_bypass_total": stats.RecoveryBypass,
		"panel_rate_limit_active_buckets":        stats.ActiveBuckets,
	}
}

func trimGatewayLitePanelWindow(values []time.Time, cutoff time.Time) []time.Time {
	index := 0
	for index < len(values) && !values[index].After(cutoff) {
		index++
	}
	if index == 0 {
		return values
	}
	return values[index:]
}

func gatewayLitePanelIdentity(c *gin.Context) string {
	if subject, ok := GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		return "user:" + strconv.FormatInt(subject.UserID, 10)
	}
	ip := strings.TrimSpace(c.ClientIP())
	if ip == "" {
		ip = "unknown"
	}
	// IP 只用作内存限流键，不会进入 ingress 聚合、数据库或健康上报。
	return fmt.Sprintf("admin-key:%s", ip)
}

func gatewayLitePanelHeavyPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	return strings.Contains(path, "/admin/dashboard") || strings.Contains(path, "/admin/ops/dashboard") || strings.Contains(path, "/admin/usage")
}

func gatewayLitePanelRecoveryPath(path string) bool {
	path = strings.TrimRight(strings.ToLower(strings.TrimSpace(path)), "/")
	return path == "/api/v1/admin/ops/runtime/panel-rate-limit" ||
		path == "/api/v1/admin/ops/runtime/panel-rate-limit/reset"
}

var activeGatewayLitePanelRateLimiter atomic.Pointer[GatewayLitePanelRateLimiter]

func ActiveGatewayLitePanelRateLimiter() *GatewayLitePanelRateLimiter {
	return activeGatewayLitePanelRateLimiter.Load()
}

func ActiveGatewayLitePanelRateLimitStats() GatewayLitePanelRateLimitRuntimeStats {
	if limiter := ActiveGatewayLitePanelRateLimiter(); limiter != nil {
		return limiter.Stats()
	}
	return GatewayLitePanelRateLimitRuntimeStats{}
}
