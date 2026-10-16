package service

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

const openAISchedulerNeutralSoftFactor = 0.5

// Fixed daily/weekly reset timestamps are computed at most seven days ahead.
// Requiring an explicit observation timestamp prevents a copied or otherwise
// stale future reset from remaining a trusted scheduling signal forever.
const openAIQuotaResetSnapshotStaleAfter = 8 * 24 * time.Hour

// applyOpenAISchedulerSoftSignals only consumes the immutable settings snapshot
// and account.Extra already present in the scheduler snapshot. It must never add
// a database/Redis/HTTP round trip to the customer request path.
func applyOpenAISchedulerSoftSignals(candidates []openAIAccountCandidateScore, settings UpstreamBillingRuntimeSettings, now time.Time) {
	costWeight := settings.UpstreamCostWeight
	if !settings.CostSchedulingEnabled {
		costWeight = 0
	}
	if costWeight <= 0 && settings.QuotaHeadroomWeight <= 0 && settings.PreferSoonestResetWeight <= 0 {
		return
	}

	for i := range candidates {
		candidates[i].upstreamCostFactor = openAISchedulerNeutralSoftFactor
		candidates[i].quotaHeadroomFactor = openAISchedulerNeutralSoftFactor
		candidates[i].soonestResetFactor = openAISchedulerNeutralSoftFactor
	}

	priorityBuckets := make(map[int][]int)
	if costWeight > 0 || settings.PreferSoonestResetWeight > 0 {
		for i := range candidates {
			priority := openAIAccountCandidateSchedulingPriority(candidates[i])
			priorityBuckets[priority] = append(priorityBuckets[priority], i)
		}
	}
	if costWeight > 0 {
		applyOpenAIUpstreamCostFactors(candidates, priorityBuckets, now)
	}
	if settings.QuotaHeadroomWeight > 0 {
		for i := range candidates {
			if factor, known := openAIQuotaHeadroomFactor(candidates[i].account, now); known {
				candidates[i].quotaHeadroomFactor = factor
			}
		}
	}
	if settings.PreferSoonestResetWeight > 0 {
		applyOpenAISoonestResetFactors(candidates, priorityBuckets, now)
	}

	for i := range candidates {
		candidates[i].breakdown.UpstreamCost = costWeight * candidates[i].upstreamCostFactor
		candidates[i].breakdown.QuotaHeadroom = settings.QuotaHeadroomWeight * candidates[i].quotaHeadroomFactor
		candidates[i].breakdown.SoonestReset = settings.PreferSoonestResetWeight * candidates[i].soonestResetFactor
		candidates[i].score += candidates[i].breakdown.UpstreamCost +
			candidates[i].breakdown.QuotaHeadroom +
			candidates[i].breakdown.SoonestReset
	}
}

func applyOpenAIUpstreamCostFactors(candidates []openAIAccountCandidateScore, buckets map[int][]int, now time.Time) {
	for _, indexes := range buckets {
		minValue, maxValue := 0.0, 0.0
		knownCount := 0
		for _, index := range indexes {
			value, known := openAIUpstreamPurchaseCost(candidates[index].account, now)
			if !known {
				continue
			}
			candidates[index].upstreamCostValue = value
			candidates[index].upstreamCostKnown = true
			if knownCount == 0 || value < minValue {
				minValue = value
			}
			if knownCount == 0 || value > maxValue {
				maxValue = value
			}
			knownCount++
		}
		// A single known value, equal values, and all missing values are neutral;
		// there is no trustworthy relative ordering to infer.
		if knownCount < 2 || maxValue <= minValue {
			continue
		}
		for _, index := range indexes {
			if candidates[index].upstreamCostKnown {
				candidates[index].upstreamCostFactor = 1 - clamp01((candidates[index].upstreamCostValue-minValue)/(maxValue-minValue))
			}
		}
	}
}

func openAIUpstreamPurchaseCost(account *Account, now time.Time) (float64, bool) {
	if !isSub2APIUpstreamAccount(account) {
		return 0, false
	}
	snapshot := DecodeUpstreamBillingProbeSnapshot(account.Extra)
	if snapshot == nil || snapshot.Status == UpstreamBillingProbeStatusUnsupported || snapshot.FreshUntil == nil || !now.Before(*snapshot.FreshUntil) {
		return 0, false
	}
	// Config changes delete this server-managed snapshot before scheduler refresh,
	// and probe writes use CAS. Re-hashing credentials for every candidate would
	// put secret-bearing JSON work on the hot path, so presence of the server
	// fingerprint is the cheap invariant checked here.
	if snapshot.ConfigFingerprint == "" {
		return 0, false
	}
	if snapshot.Status != UpstreamBillingProbeStatusOK && snapshot.Status != UpstreamBillingProbeStatusFailed {
		return 0, false
	}
	resolved, ok := finiteExtraNumber(snapshot.Data, "resolved_rate_multiplier")
	if !ok || resolved < 0 {
		return 0, false
	}
	peakEnabled, ok := snapshot.Data["peak_rate_enabled"].(bool)
	if !ok {
		return 0, false
	}
	if !peakEnabled {
		return resolved, true
	}
	peakRate, ok := finiteExtraNumber(snapshot.Data, "peak_rate_multiplier")
	if !ok || peakRate < 0 {
		return 0, false
	}
	start, startOK := parseBillingMinute(extraString(snapshot.Data, "peak_start"))
	end, endOK := parseBillingMinute(extraString(snapshot.Data, "peak_end"))
	location, err := time.LoadLocation(strings.TrimSpace(extraString(snapshot.Data, "timezone")))
	if !startOK || !endOK || start >= end || err != nil {
		return 0, false
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	if minute >= start && minute < end {
		return resolved * peakRate, true
	}
	return resolved, true
}

func openAIQuotaHeadroomFactor(account *Account, now time.Time) (float64, bool) {
	if account == nil || account.Extra == nil {
		return 0, false
	}
	remaining := 1.0
	known := false
	for _, pair := range [][2]string{
		{"quota_limit", "quota_used"},
		{"quota_daily_limit", "quota_daily_used"},
		{"quota_weekly_limit", "quota_weekly_used"},
	} {
		limit, limitOK := finiteExtraNumber(account.Extra, pair[0])
		used, usedOK := finiteExtraNumber(account.Extra, pair[1])
		if !limitOK || !usedOK || limit <= 0 || used < 0 {
			continue
		}
		factor := clamp01((limit - used) / limit)
		if !known || factor < remaining {
			remaining = factor
		}
		known = true
	}
	for _, window := range []string{"5h", "7d"} {
		if !codexWindowSnapshotFresh(account.Extra, window, now, openAICodexAutoPauseStaleAfter) {
			continue
		}
		for _, key := range []string{"codex_" + window + "_used_percent"} {
			used, ok := finiteExtraNumber(account.Extra, key)
			if !ok || used < 0 || used > 100 {
				continue
			}
			factor := clamp01(1 - used/100)
			if !known || factor < remaining {
				remaining = factor
			}
			known = true
		}
	}
	return remaining, known
}

func applyOpenAISoonestResetFactors(candidates []openAIAccountCandidateScore, buckets map[int][]int, now time.Time) {
	for _, indexes := range buckets {
		var earliest, latest time.Time
		knownCount := 0
		for _, index := range indexes {
			resetAt, known := openAISoonestTrustedReset(candidates[index].account, now)
			if !known {
				continue
			}
			candidates[index].soonestResetAt = resetAt
			candidates[index].soonestResetKnown = true
			knownCount++
			if earliest.IsZero() || resetAt.Before(earliest) {
				earliest = resetAt
			}
			if latest.IsZero() || resetAt.After(latest) {
				latest = resetAt
			}
		}
		if knownCount < 2 || !latest.After(earliest) {
			continue
		}
		span := latest.Sub(earliest).Seconds()
		for _, index := range indexes {
			if candidates[index].soonestResetKnown {
				candidates[index].soonestResetFactor = 1 - clamp01(candidates[index].soonestResetAt.Sub(earliest).Seconds()/span)
			}
		}
	}
}

func openAISoonestTrustedReset(account *Account, now time.Time) (time.Time, bool) {
	if account == nil || account.Extra == nil {
		return time.Time{}, false
	}
	var earliest time.Time
	consider := func(value time.Time) {
		if value.After(now) && (earliest.IsZero() || value.Before(earliest)) {
			earliest = value
		}
	}
	if quotaResetSnapshotFresh(account.Extra, now) {
		for _, key := range []string{"quota_daily_reset_at", "quota_weekly_reset_at"} {
			if value, ok := parseExtraTimestamp(account.Extra[key]); ok {
				consider(value)
			}
		}
	}
	for _, window := range []string{"5h", "7d"} {
		if !codexWindowSnapshotFresh(account.Extra, window, now, openAICodexAutoPauseStaleAfter) {
			continue
		}
		for _, key := range []string{"codex_" + window + "_reset_at"} {
			if value, ok := parseExtraTimestamp(account.Extra[key]); ok {
				consider(value)
			}
		}
	}
	return earliest, !earliest.IsZero()
}

func quotaResetSnapshotFresh(extra map[string]any, now time.Time) bool {
	updatedAt, ok := parseExtraTimestamp(extra["quota_reset_updated_at"])
	if !ok || updatedAt.After(now.Add(5*time.Minute)) {
		return false
	}
	return now.Sub(updatedAt) <= openAIQuotaResetSnapshotStaleAfter
}

func codexUsageSnapshotFresh(extra map[string]any, now time.Time) bool {
	return codexWindowSnapshotFresh(extra, "5h", now, openAICodexAutoPauseStaleAfter) || codexWindowSnapshotFresh(extra, "7d", now, openAICodexAutoPauseStaleAfter)
}

// codexWindowUpdatedAt 返回指定窗口的采样时间。
// 新格式会为 5h/7d 分别写入时间戳；只要任一窗口专属键已经出现，缺失的
// 另一窗口就不能再借用全局时间戳，否则不同窗口的新旧快照会被错误混合。
// 仅对完全没有窗口专属键的历史数据回退 codex_usage_updated_at。
func codexWindowUpdatedAt(extra map[string]any, window string) (time.Time, bool) {
	if len(extra) == 0 || (window != "5h" && window != "7d") {
		return time.Time{}, false
	}
	windowKey := "codex_" + window + "_usage_updated_at"
	if raw, exists := extra[windowKey]; exists {
		return parseExtraTimestamp(raw)
	}
	if _, hasFiveHour := extra["codex_5h_usage_updated_at"]; hasFiveHour {
		return time.Time{}, false
	}
	if _, hasSevenDay := extra["codex_7d_usage_updated_at"]; hasSevenDay {
		return time.Time{}, false
	}
	return parseExtraTimestamp(extra["codex_usage_updated_at"])
}

func parseExtraTimestamp(raw any) (time.Time, bool) {
	switch value := raw.(type) {
	case time.Time:
		return value, !value.IsZero()
	case *time.Time:
		if value != nil && !value.IsZero() {
			return *value, true
		}
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		return parsed, err == nil && !parsed.IsZero()
	}
	return time.Time{}, false
}

func finiteExtraNumber(extra map[string]any, key string) (float64, bool) {
	if extra == nil {
		return 0, false
	}
	raw, exists := extra[key]
	if !exists || raw == nil {
		return 0, false
	}
	var value float64
	switch number := raw.(type) {
	case float64:
		value = number
	case float32:
		value = float64(number)
	case int:
		value = float64(number)
	case int64:
		value = float64(number)
	case json.Number:
		parsed, err := number.Float64()
		if err != nil {
			return 0, false
		}
		value = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		if err != nil {
			return 0, false
		}
		value = parsed
	default:
		return 0, false
	}
	return value, !math.IsNaN(value) && !math.IsInf(value, 0)
}

// [v0.2.5 新增] 解析 "HH:MM"（或 RFC3339）为当分钟序号；失败返回 false。
func parseBillingMinute(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	for _, layout := range []string{"15:04", "2006-01-02T15:04:05Z07:00", time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Hour()*60 + t.Minute(), true
		}
	}
	return 0, false
}

// [v0.2.5 新增] extraString 读取 map 中的字符串字段。
func extraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	if v, ok := extra[key]; ok {
		if str, ok := v.(string); ok {
			return strings.TrimSpace(str)
		}
	}
	return ""
}
