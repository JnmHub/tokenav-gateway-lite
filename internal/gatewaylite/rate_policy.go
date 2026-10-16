package gatewaylite

import (
	"math"
	"strings"
)

// RatePolicyAudit 是请求开始时冻结的销售倍率证据。
//
// 上游账号成本倍率不属于这份结构，绝不能再次乘入客户钱包扣费。
// ResolvedRateMultiplier 表示“用户专属倍率覆盖分组默认倍率”后的基础值；
// EffectiveRateMultiplier 才是 Token 计费最终使用的值。
type RatePolicyAudit struct {
	SchemaVersion           int      `json:"schema_version"`
	BillingScope            string   `json:"billing_scope"`
	GroupRateMultiplier     float64  `json:"group_rate_multiplier"`
	UserRateMultiplier      *float64 `json:"user_rate_multiplier,omitempty"`
	ResolvedRateMultiplier  float64  `json:"resolved_rate_multiplier"`
	PeakRateEnabled         bool     `json:"peak_rate_enabled"`
	PeakStart               string   `json:"peak_start,omitempty"`
	PeakEnd                 string   `json:"peak_end,omitempty"`
	PeakRateMultiplier      float64  `json:"peak_rate_multiplier,omitempty"`
	AppliedPeakMultiplier   float64  `json:"applied_peak_multiplier"`
	EffectiveRateMultiplier float64  `json:"effective_rate_multiplier"`
	Timezone                string   `json:"timezone"`
	ResolvedAtMillis        int64    `json:"resolved_at_millis"`
	Version                 string   `json:"version"`
}

func (p RatePolicyAudit) Clone() RatePolicyAudit {
	cloned := p
	if p.UserRateMultiplier != nil {
		value := *p.UserRateMultiplier
		cloned.UserRateMultiplier = &value
	}
	return cloned
}

func (p RatePolicyAudit) Valid() bool {
	if p.SchemaVersion != 1 || p.BillingScope != "token" ||
		!validRatePolicyVersion(p.Version) || p.ResolvedAtMillis <= 0 ||
		strings.TrimSpace(p.Timezone) == "" || strings.TrimSpace(p.Timezone) != p.Timezone {
		return false
	}
	multipliers := []float64{
		p.GroupRateMultiplier,
		p.ResolvedRateMultiplier,
		p.PeakRateMultiplier,
		p.AppliedPeakMultiplier,
		p.EffectiveRateMultiplier,
	}
	for _, multiplier := range multipliers {
		if !finiteNonNegativeRate(multiplier) {
			return false
		}
	}
	resolved := p.GroupRateMultiplier
	if p.UserRateMultiplier != nil {
		if !finiteNonNegativeRate(*p.UserRateMultiplier) {
			return false
		}
		resolved = *p.UserRateMultiplier
	}
	if !sameRatePolicyFloat(p.ResolvedRateMultiplier, resolved) {
		return false
	}
	effective := p.ResolvedRateMultiplier * p.AppliedPeakMultiplier
	if !finiteNonNegativeRate(effective) || !sameRatePolicyFloat(p.EffectiveRateMultiplier, effective) {
		return false
	}
	if !p.PeakRateEnabled {
		return sameRatePolicyFloat(p.AppliedPeakMultiplier, 1)
	}
	return p.PeakRateMultiplier > 0 &&
		(sameRatePolicyFloat(p.AppliedPeakMultiplier, 1) || sameRatePolicyFloat(p.AppliedPeakMultiplier, p.PeakRateMultiplier))
}

func finiteNonNegativeRate(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sameRatePolicyFloat(left, right float64) bool {
	delta := math.Abs(left - right)
	scale := math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
	return delta <= scale*1e-12
}

func validRatePolicyVersion(value string) bool {
	if len(value) != len("v1:")+16 || !strings.HasPrefix(value, "v1:") {
		return false
	}
	for _, char := range value[len("v1:"):] {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}
