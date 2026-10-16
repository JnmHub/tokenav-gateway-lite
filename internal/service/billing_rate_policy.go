package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
	ratePolicySchemaVersion = 1
	defaultPeakTimezone     = "Asia/Shanghai"
	defaultPeakStart        = "18:00"
	defaultPeakEnd          = "22:00"
	maxPeakRateMultiplier   = 100.0
)

type effectiveRatePolicyContextKey struct{}

// ContextWithEffectiveRatePolicy freezes one request's sales multiplier before
// any upstream work starts. Streaming completion and async settlement must read
// this immutable copy instead of recalculating from a later wall-clock time.
func ContextWithEffectiveRatePolicy(ctx context.Context, policy gatewaylite.RatePolicyAudit) context.Context {
	if ctx == nil || !policy.Valid() {
		return ctx
	}
	// UserRateMultiplier 是指针，安装与读取都复制，避免管理编辑或调用方修改污染执行倍率。
	policy = policy.Clone()
	ctx = gatewaylite.ContextWithRatePolicyAudit(ctx, policy)
	return context.WithValue(ctx, effectiveRatePolicyContextKey{}, policy)
}

func FreezeEffectiveRatePolicy(ctx context.Context, group *Group, userRate *float64, now time.Time) (context.Context, gatewaylite.RatePolicyAudit, error) {
	policy, err := ResolveEffectiveRate(group, userRate, now)
	if err != nil {
		return ctx, gatewaylite.RatePolicyAudit{}, err
	}
	return ContextWithEffectiveRatePolicy(ctx, policy), policy, nil
}

// freezeRatePolicyForReroutedAPIKey is only used after a pre-dispatch policy
// intentionally replaces the request group. The new (user, group) override is
// read authoritatively once instead of consulting the short-lived resolver
// cache: reroutes are rare, while charging an old override after an admin edit
// is a financial correctness bug. Ordinary requests still use the auth snapshot
// and add no database access to the hot path.
func freezeRatePolicyForReroutedAPIKey(ctx context.Context, cfg *config.Config, repo UserGroupRateRepository, apiKey *APIKey, now time.Time) (context.Context, error) {
	if apiKey == nil || apiKey.Group == nil {
		return ctx, errors.New("billing group is unavailable")
	}
	var userRate *float64
	if shouldResolveLocalUserGroupRate(ctx, cfg) && apiKey.User != nil && repo != nil {
		resolved, err := repo.GetByUserAndGroup(ctx, apiKey.User.ID, apiKey.Group.ID)
		if err != nil {
			return ctx, fmt.Errorf("get rerouted user group rate: %w", err)
		}
		if resolved != nil {
			value := *resolved
			userRate = &value
		}
	} else if rate, ok := apiKey.UserGroupRateMultipliers[apiKey.Group.ID]; ok {
		// 远程 Key 的有界映射由主站按用户权限生成。这里只按重路由后的
		// 目标本地分组编号精确读取，绝不能沿用原固定分组覆盖值。
		value := rate
		userRate = &value
	}
	apiKey.UserRateMultiplier = userRate
	frozen, _, err := FreezeEffectiveRatePolicy(ctx, apiKey.Group, userRate, now)
	return frozen, err
}

func EffectiveRatePolicyFromContext(ctx context.Context) (gatewaylite.RatePolicyAudit, bool) {
	if ctx == nil {
		return gatewaylite.RatePolicyAudit{}, false
	}
	policy, ok := ctx.Value(effectiveRatePolicyContextKey{}).(gatewaylite.RatePolicyAudit)
	if ok && policy.Valid() {
		return policy.Clone(), true
	}
	if meta, exists := gatewaylite.RequestMetaFromContext(ctx); exists && meta.RatePolicyAudit != nil && meta.RatePolicyAudit.Valid() {
		return meta.RatePolicyAudit.Clone(), true
	}
	return gatewaylite.RatePolicyAudit{}, false
}

func normalizePeakTimezone(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "local") {
		if configured := strings.TrimSpace(timezone.Name()); configured != "" && !strings.EqualFold(configured, "local") {
			return configured
		}
		return defaultPeakTimezone
	}
	return name
}

// parsePeakMinutes parses a small, allocation-free HH:MM value used on the
// request billing hot path. One-digit hours remain accepted for old data.
func parsePeakMinutes(value string) (int, bool) {
	value = strings.TrimSpace(value)
	colon := strings.IndexByte(value, ':')
	if (colon != 1 && colon != 2) || len(value)-colon-1 != 2 {
		return 0, false
	}
	hour := 0
	for i := 0; i < colon; i++ {
		digit := value[i] - '0'
		if digit > 9 {
			return 0, false
		}
		hour = hour*10 + int(digit)
	}
	minute1, minute2 := value[colon+1]-'0', value[colon+2]-'0'
	if minute1 > 9 || minute2 > 9 {
		return 0, false
	}
	minute := int(minute1)*10 + int(minute2)
	if hour > 23 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

// ValidatePeakRateConfig is the single write-path validator. start > end is a
// valid cross-midnight window, while equal endpoints are rejected as ambiguous.
func ValidatePeakRateConfig(enabled bool, start, end string, multiplier float64, timezoneName string) error {
	if !enabled {
		return nil
	}
	startMinute, startOK := parsePeakMinutes(start)
	endMinute, endOK := parsePeakMinutes(end)
	if !startOK || !endOK {
		return errors.New("peak_start and peak_end must use HH:MM")
	}
	if startMinute == endMinute {
		return errors.New("peak_start and peak_end must be different")
	}
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 || multiplier > maxPeakRateMultiplier {
		return fmt.Errorf("peak_rate_multiplier must be greater than 0 and at most %.0f", maxPeakRateMultiplier)
	}
	if _, err := time.LoadLocation(normalizePeakTimezone(timezoneName)); err != nil {
		return errors.New("peak_timezone must be a valid IANA timezone")
	}
	return nil
}

// NormalizePeakRateConfig keeps a disabled but valid schedule for easy
// re-enabling. Invalid disabled values are cleared so stale data cannot become
// active after a future partial update.
func NormalizePeakRateConfig(enabled bool, start, end string, multiplier float64, timezoneName string) (bool, string, string, float64, string) {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	timezoneName = normalizePeakTimezone(timezoneName)
	if _, err := time.LoadLocation(timezoneName); err != nil {
		timezoneName = defaultPeakTimezone
	}
	if !enabled {
		if _, ok := parsePeakMinutes(start); !ok {
			start = defaultPeakStart
		}
		if _, ok := parsePeakMinutes(end); !ok {
			end = defaultPeakEnd
		}
		if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 || multiplier > maxPeakRateMultiplier {
			multiplier = 1
		}
	}
	return enabled, start, end, multiplier, timezoneName
}

// PeakMultiplierAt returns the group factor for [start,end). Cross-midnight
// windows use cur>=start || cur<end. IANA timezone conversion also makes DST
// changes deterministic without storing a fixed UTC offset.
func (g *Group) PeakMultiplierAt(now time.Time) float64 {
	if g == nil || !g.PeakRateEnabled {
		return 1
	}
	start, startOK := parsePeakMinutes(g.PeakStart)
	end, endOK := parsePeakMinutes(g.PeakEnd)
	if !startOK || !endOK || start == end || g.PeakRateMultiplier <= 0 ||
		math.IsNaN(g.PeakRateMultiplier) || math.IsInf(g.PeakRateMultiplier, 0) {
		return 1
	}
	location, err := time.LoadLocation(normalizePeakTimezone(g.PeakTimezone))
	if err != nil {
		return 1
	}
	local := now.In(location)
	current := local.Hour()*60 + local.Minute()
	if (start < end && current >= start && current < end) ||
		(start > end && (current >= start || current < end)) {
		return g.PeakRateMultiplier
	}
	return 1
}

// ResolveEffectiveRate is the only sales multiplier resolver:
// group default -> optional user override -> peak factor. Account/upstream cost
// multipliers are deliberately absent and therefore cannot leak into billing.
func ResolveEffectiveRate(group *Group, userRate *float64, now time.Time) (gatewaylite.RatePolicyAudit, error) {
	if group == nil {
		return gatewaylite.RatePolicyAudit{}, errors.New("billing group is unavailable")
	}
	groupRate := group.RateMultiplier
	if math.IsNaN(groupRate) || math.IsInf(groupRate, 0) || groupRate < 0 {
		return gatewaylite.RatePolicyAudit{}, errors.New("group rate multiplier is invalid")
	}
	resolvedRate := groupRate
	var normalizedUserRate *float64
	if userRate != nil {
		if math.IsNaN(*userRate) || math.IsInf(*userRate, 0) || *userRate < 0 {
			return gatewaylite.RatePolicyAudit{}, errors.New("user rate multiplier is invalid")
		}
		value := *userRate
		normalizedUserRate = &value
		resolvedRate = value
	}
	if err := ValidatePeakRateConfig(group.PeakRateEnabled, group.PeakStart, group.PeakEnd, group.PeakRateMultiplier, group.PeakTimezone); err != nil {
		return gatewaylite.RatePolicyAudit{}, err
	}
	peakTimezone := normalizePeakTimezone(group.PeakTimezone)
	appliedPeak := group.PeakMultiplierAt(now)
	userRateVersion := "none"
	if normalizedUserRate != nil {
		// 不能直接格式化指针；指针地址会让同一配置在每个请求上产生不同版本，
		// 失去审计和对账意义。这里只把倍率值写入哈希输入。
		userRateVersion = fmt.Sprintf("%.12g", *normalizedUserRate)
	}
	versionInput := fmt.Sprintf("%d|%.12g|%s|%.12g|%t|%s|%s|%.12g|%s|%d",
		group.ID, groupRate, userRateVersion, resolvedRate, group.PeakRateEnabled,
		strings.TrimSpace(group.PeakStart), strings.TrimSpace(group.PeakEnd), group.PeakRateMultiplier,
		peakTimezone, group.UpdatedAt.UnixNano())
	digest := sha256.Sum256([]byte(versionInput))
	policy := gatewaylite.RatePolicyAudit{
		SchemaVersion:           ratePolicySchemaVersion,
		BillingScope:            "token",
		GroupRateMultiplier:     groupRate,
		UserRateMultiplier:      normalizedUserRate,
		ResolvedRateMultiplier:  resolvedRate,
		PeakRateEnabled:         group.PeakRateEnabled,
		PeakStart:               strings.TrimSpace(group.PeakStart),
		PeakEnd:                 strings.TrimSpace(group.PeakEnd),
		PeakRateMultiplier:      group.PeakRateMultiplier,
		AppliedPeakMultiplier:   appliedPeak,
		EffectiveRateMultiplier: resolvedRate * appliedPeak,
		Timezone:                peakTimezone,
		ResolvedAtMillis:        now.UTC().UnixMilli(),
		Version:                 fmt.Sprintf("v1:%x", digest[:8]),
	}
	return policy, nil
}
