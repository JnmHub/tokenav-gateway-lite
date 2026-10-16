package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	// GrokMediaEligibleExtraKey 是管理员三态覆写：true 强制允许，false
	// 强制隔离，nil/缺失回到自动探测。它不修改账号的文本健康状态。
	GrokMediaEligibleExtraKey = "grok_media_eligible"
	// GrokMediaEligibilitySnapshotExtraKey 保存最近探测和最后一次确定性资格。
	// 最近探测失败时保留 Eligible，另用 Stale 标明证据已陈旧。
	GrokMediaEligibilitySnapshotExtraKey = "grok_media_eligibility_snapshot"

	grokMediaEligibilityFreshTTL = 10 * time.Minute
)

type GrokMediaEligibilitySnapshot struct {
	Eligible          *bool    `json:"eligible,omitempty"`
	Source            string   `json:"source,omitempty"`
	ObservedAt        string   `json:"observed_at,omitempty"`
	ProbedAt          string   `json:"probed_at,omitempty"`
	Stale             bool     `json:"stale,omitempty"`
	Partial           bool     `json:"partial,omitempty"`
	WeeklyStatusCode  int      `json:"weekly_status_code,omitempty"`
	MonthlyStatusCode int      `json:"monthly_status_code,omitempty"`
	FailedWindows     []string `json:"failed_windows,omitempty"`
}

// GrokMediaEligibilityDetails 是管理员接口的可解释视图。Eligible 为 nil
// 表示自动资格未知；EffectiveEligible 表示调度的兼容行为（未知时继续放行）。
type GrokMediaEligibilityDetails struct {
	State             string   `json:"state"`
	Eligible          *bool    `json:"eligible"`
	EffectiveEligible bool     `json:"effective_eligible"`
	Source            string   `json:"source"`
	Reason            string   `json:"reason"`
	Override          *bool    `json:"override"`
	ObservedAt        string   `json:"observed_at,omitempty"`
	ProbedAt          string   `json:"probed_at,omitempty"`
	Stale             bool     `json:"stale"`
	WeeklyStatusCode  int      `json:"weekly_status_code,omitempty"`
	MonthlyStatusCode int      `json:"monthly_status_code,omitempty"`
	FailedWindows     []string `json:"failed_windows,omitempty"`
}

// GrokMediaGenerationEligibility 只判断“能否接收新的媒体生成”。未知证据保持
// 旧版本的兼容放行；视频状态查询不调用该方法，因此历史任务仍可查询。
func (a *Account) GrokMediaGenerationEligibility() (bool, string) {
	details := a.GrokMediaGenerationEligibilityDetails()
	return details.EffectiveEligible, details.Reason
}

func (a *Account) GrokMediaGenerationEligibilityDetails(nowValues ...time.Time) GrokMediaEligibilityDetails {
	now := time.Now().UTC()
	if len(nowValues) > 0 && !nowValues[0].IsZero() {
		now = nowValues[0].UTC()
	}
	details := GrokMediaEligibilityDetails{
		State:             "unknown",
		EffectiveEligible: true,
		Source:            "automatic",
		Reason:            "billing_unobserved",
	}
	if a == nil || !a.IsGrok() {
		details.EffectiveEligible = false
		details.Source = "not_grok"
		details.Reason = "not_grok"
		return details
	}
	if override, ok := grokMediaEligibilityOverride(a.Extra); ok {
		details.Override = boolPointer(override)
		details.Eligible = boolPointer(override)
		details.EffectiveEligible = override
		details.Source = "override"
		if override {
			details.State = "eligible"
			details.Reason = "override_enabled"
		} else {
			details.State = "ineligible"
			details.Reason = "override_disabled"
		}
		return details
	}
	// 媒体提交本身返回的确定性 403 同样适用于 API Key 账号。必须先读取
	// 该证据，再使用“非 OAuth 默认可用”的兼容规则，否则同一无媒体资格
	// 的 API Key 会在每个请求上反复触发上游收费端点，并可能污染文本健康。
	if snapshot, err := grokMediaEligibilitySnapshotFromExtra(a.Extra); err == nil && snapshot != nil {
		details.Source = firstNonEmpty(strings.TrimSpace(snapshot.Source), "billing_probe")
		details.ObservedAt = strings.TrimSpace(snapshot.ObservedAt)
		details.ProbedAt = strings.TrimSpace(snapshot.ProbedAt)
		details.Stale = snapshot.Stale || grokMediaEligibilityTimestampStale(details.ProbedAt, now)
		details.WeeklyStatusCode = snapshot.WeeklyStatusCode
		details.MonthlyStatusCode = snapshot.MonthlyStatusCode
		details.FailedWindows = append([]string(nil), snapshot.FailedWindows...)
		if snapshot.Eligible != nil {
			eligible := *snapshot.Eligible
			details.Eligible = boolPointer(eligible)
			details.EffectiveEligible = eligible
			if eligible {
				details.State = "eligible"
				details.Reason = "eligible"
			} else {
				details.State = "ineligible"
				details.Reason = "billing_forbidden"
				if details.Source == "media_request" {
					details.Reason = "media_request_forbidden"
				}
			}
		}
		return details
	}
	if a.Type != AccountTypeOAuth {
		details.State = "eligible"
		details.Eligible = boolPointer(true)
		details.EffectiveEligible = true
		details.Source = "non_oauth_default"
		details.Reason = "non_oauth"
		return details
	}

	// 升级前已经存在的 billing snapshot 仍能立刻提供兼容证据；下一次
	// 探测会补写独立资格快照。
	if billing, err := grokBillingSnapshotFromExtra(a.Extra); err == nil && billing != nil {
		details.Source = "billing_snapshot_legacy"
		details.ProbedAt = firstNonEmpty(strings.TrimSpace(billing.UpdatedAt), strings.TrimSpace(billing.FetchedAt))
		details.Stale = billing.Partial || len(billing.FailedWindows) > 0 || grokMediaEligibilityTimestampStale(details.ProbedAt, now)
		details.WeeklyStatusCode = billing.WeeklyStatusCode
		details.MonthlyStatusCode = billing.MonthlyStatusCode
		details.FailedWindows = append([]string(nil), billing.FailedWindows...)
		switch {
		case billing.StatusCode == http.StatusForbidden || billing.WeeklyStatusCode == http.StatusForbidden || billing.MonthlyStatusCode == http.StatusForbidden:
			details.State = "ineligible"
			details.Eligible = boolPointer(false)
			details.EffectiveEligible = false
			details.Reason = "billing_forbidden"
		case successfulHTTPStatus(billing.WeeklyStatusCode) && successfulHTTPStatus(billing.MonthlyStatusCode):
			details.State = "eligible"
			details.Eligible = boolPointer(true)
			details.Reason = "eligible"
		}
	}
	return details
}

func grokMediaEligibilityOverride(extra map[string]any) (bool, bool) {
	if extra == nil {
		return false, false
	}
	raw, exists := extra[GrokMediaEligibleExtraKey]
	if !exists || raw == nil {
		return false, false
	}
	value, ok := raw.(bool)
	return value, ok
}

func grokMediaEligibilitySnapshotFromExtra(extra map[string]any) (*GrokMediaEligibilitySnapshot, error) {
	if extra == nil {
		return nil, nil
	}
	raw, exists := extra[GrokMediaEligibilitySnapshotExtraKey]
	if !exists || raw == nil {
		return nil, nil
	}
	switch snapshot := raw.(type) {
	case *GrokMediaEligibilitySnapshot:
		return snapshot, nil
	case GrokMediaEligibilitySnapshot:
		return &snapshot, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var out GrokMediaEligibilitySnapshot
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
}

func buildGrokMediaEligibilitySnapshot(
	previous *GrokMediaEligibilitySnapshot,
	weeklyStatus int,
	weeklyErr error,
	monthlyStatus int,
	monthlyErr error,
	now time.Time,
) *GrokMediaEligibilitySnapshot {
	var out GrokMediaEligibilitySnapshot
	if previous != nil {
		out = *previous
		out.Eligible = cloneBoolPointer(previous.Eligible)
		out.FailedWindows = append([]string(nil), previous.FailedWindows...)
	}
	nowText := now.UTC().Format(time.RFC3339)
	out.Source = "billing_probe"
	out.ProbedAt = nowText
	out.WeeklyStatusCode = weeklyStatus
	out.MonthlyStatusCode = monthlyStatus
	weeklyAllowed := weeklyErr == nil && successfulHTTPStatus(weeklyStatus)
	monthlyAllowed := monthlyErr == nil && successfulHTTPStatus(monthlyStatus)
	out.Partial = !weeklyAllowed || !monthlyAllowed
	out.FailedWindows = nil
	if !weeklyAllowed {
		out.FailedWindows = append(out.FailedWindows, "weekly")
	}
	if !monthlyAllowed {
		out.FailedWindows = append(out.FailedWindows, "monthly")
	}

	switch {
	case weeklyStatus == http.StatusForbidden || monthlyStatus == http.StatusForbidden:
		out.Eligible = boolPointer(false)
		out.ObservedAt = nowText
		out.Stale = false
	case weeklyAllowed && monthlyAllowed:
		out.Eligible = boolPointer(true)
		out.ObservedAt = nowText
		out.Stale = false
	default:
		// 429、5xx、超时和解析错误都不是资格证据。保留最后一次确定性
		// 结果；若从未成功探测，则 Eligible 继续为 nil（未知）。
		out.Stale = true
	}
	return &out
}

// buildGrokMediaRequestForbiddenSnapshot records a definitive entitlement
// rejection from an actual media submission. Unlike a billing probe, this
// evidence has no weekly/monthly window status and must never change the
// account's text scheduling state.
func buildGrokMediaRequestForbiddenSnapshot(previous *GrokMediaEligibilitySnapshot, now time.Time) *GrokMediaEligibilitySnapshot {
	var out GrokMediaEligibilitySnapshot
	if previous != nil {
		out = *previous
	}
	nowText := now.UTC().Format(time.RFC3339)
	out.Eligible = boolPointer(false)
	out.Source = "media_request"
	out.ObservedAt = nowText
	out.ProbedAt = nowText
	out.Stale = false
	out.Partial = false
	out.WeeklyStatusCode = 0
	out.MonthlyStatusCode = 0
	out.FailedWindows = nil
	return &out
}

func grokMediaEligibilityTimestampStale(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	observedAt, err := time.Parse(time.RFC3339, raw)
	return err != nil || now.Sub(observedAt) >= grokMediaEligibilityFreshTTL
}

func successfulHTTPStatus(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

func boolPointer(value bool) *bool {
	return &value
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	return boolPointer(*value)
}
