package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

const grokQuotaSnapshotExtraKey = "grok_usage_snapshot"

type GrokQuotaFetcher struct{}

func NewGrokQuotaFetcher() *GrokQuotaFetcher {
	return &GrokQuotaFetcher{}
}

// GrokQuotaSnapshotForAccount returns the latest sanitized xAI quota snapshot
// for administrator-facing diagnostics. The snapshot contains only the
// allowlisted rate-limit and entitlement headers persisted by the gateway.
func GrokQuotaSnapshotForAccount(account *Account) *xai.QuotaSnapshot {
	if account == nil {
		return nil
	}
	snapshot, err := grokQuotaSnapshotFromExtra(account.Extra)
	if err != nil {
		return nil
	}
	return snapshot
}

func (f *GrokQuotaFetcher) BuildUsageInfo(account *Account) *UsageInfo {
	now := time.Now()
	usage := &UsageInfo{
		Source:    "passive",
		UpdatedAt: &now,
	}
	if account == nil {
		usage.ErrorCode = "quota_unknown"
		usage.Error = "Grok quota is unknown until billing is probed or an upstream response includes xAI rate-limit headers"
		return usage
	}

	billing, _ := grokBillingSnapshotFromExtra(account.Extra)
	snapshot, err := grokQuotaSnapshotFromExtra(account.Extra)
	if billing != nil {
		usage.GrokBilling = billing
		if billing.Plan != "" {
			usage.SubscriptionTier = billing.Plan
			usage.SubscriptionTierRaw = billing.Plan
		}
		if parsedAt, parseErr := time.Parse(time.RFC3339, billing.UpdatedAt); parseErr == nil {
			usage.UpdatedAt = &parsedAt
		}
		if billing.FetchedAt != "" {
			usage.GrokLastQuotaProbeAt = billing.FetchedAt
		}
		usage.GrokQuotaSnapshotState = "billing_observed"
		usage.GrokLastStatusCode = billing.StatusCode
		switch billing.StatusCode {
		case http.StatusUnauthorized:
			usage.NeedsReauth = true
			usage.ErrorCode = "unauthenticated"
		case http.StatusForbidden:
			usage.IsForbidden = true
			usage.ForbiddenType = "forbidden"
			usage.ErrorCode = "forbidden"
		case http.StatusTooManyRequests:
			usage.ErrorCode = "rate_limited"
		}
	}

	if err != nil || snapshot == nil {
		applyGrokCredentialUsageFallback(usage, account, billing, nil)
		if billing == nil {
			usage.ErrorCode = "quota_unknown"
			usage.Error = "Grok quota is unknown until billing is probed or an upstream response includes xAI rate-limit headers"
		}
		return usage
	}

	if parsedAt, parseErr := time.Parse(time.RFC3339, snapshot.UpdatedAt); parseErr == nil {
		if billing == nil || usage.UpdatedAt == nil || parsedAt.After(*usage.UpdatedAt) {
			usage.UpdatedAt = &parsedAt
		}
	}
	usage.GrokRequestQuota = snapshot.Requests
	usage.GrokTokenQuota = snapshot.Tokens
	usage.GrokRetryAfterSeconds = snapshot.RetryAfterSeconds
	if usage.SubscriptionTier == "" {
		usage.SubscriptionTier = snapshot.SubscriptionTier
		usage.SubscriptionTierRaw = snapshot.SubscriptionTier
	}
	if usage.GrokEntitlementStatus == "" {
		usage.GrokEntitlementStatus = snapshot.EntitlementStatus
	}
	if usage.GrokLastQuotaProbeAt == "" {
		usage.GrokLastQuotaProbeAt = snapshot.LastProbeAt
	}
	usage.GrokLastHeadersSeenAt = snapshot.LastHeadersSeenAt
	if snapshot.StatusCode >= http.StatusBadRequest || usage.GrokLastStatusCode == 0 {
		usage.GrokLastStatusCode = snapshot.StatusCode
	}
	if snapshot.HasObservedHeaders() {
		if usage.GrokQuotaSnapshotState == "" {
			usage.GrokQuotaSnapshotState = "observed"
		}
	} else if billing == nil {
		usage.GrokQuotaSnapshotState = "no_headers"
		usage.ErrorCode = "quota_unknown"
		usage.Error = "No xAI quota headers observed on the latest Grok probe"
	}

	if usage.ErrorCode == "" {
		switch snapshot.StatusCode {
		case http.StatusUnauthorized:
			usage.NeedsReauth = true
			usage.ErrorCode = "unauthenticated"
		case http.StatusForbidden:
			usage.IsForbidden = true
			usage.ForbiddenType = "forbidden"
			usage.ErrorCode = "forbidden"
			if usage.GrokEntitlementStatus == "" {
				usage.GrokEntitlementStatus = "forbidden"
			}
		case http.StatusTooManyRequests:
			usage.ErrorCode = "rate_limited"
		}
	}
	applyGrokCredentialUsageFallback(usage, account, billing, snapshot)
	return usage
}

func applyGrokCredentialUsageFallback(usage *UsageInfo, account *Account, billing *xai.BillingSummary, snapshot *xai.QuotaSnapshot) {
	if usage == nil || account == nil {
		return
	}
	// 当前 access token 的 JWT 档位优先于可能滞后的 billing、配额快照和
	// 持久化凭据；无 tier claim 时才保持原有回退链路。
	if tier := xai.SubscriptionTierFromJWT(account.GetCredential("access_token")); tier != "" {
		usage.SubscriptionTier = tier
		usage.SubscriptionTierRaw = tier
	} else {
		signal := strings.TrimSpace(account.GetCredential("subscription_tier"))
		if signal == "" && snapshot != nil {
			signal = strings.TrimSpace(snapshot.SubscriptionTier)
		}
		if signal == "" && billing != nil {
			signal = strings.TrimSpace(billing.Plan)
		}
		var monthlyLimit *float64
		if billing != nil {
			monthlyLimit = billing.MonthlyLimitCents
		}
		if plan := xai.CanonicalGrokPlan(monthlyLimit, signal, snapshot); plan != "" {
			usage.SubscriptionTier = plan
			usage.SubscriptionTierRaw = signal
		} else if usage.SubscriptionTier == "" && signal != "" {
			usage.SubscriptionTier = signal
			usage.SubscriptionTierRaw = signal
		}
	}
	if usage.GrokEntitlementStatus == "" {
		usage.GrokEntitlementStatus = strings.TrimSpace(account.GetCredential("entitlement_status"))
	}
}

func stampGrokQuotaSnapshotForPlan(account *Account, snapshot *xai.QuotaSnapshot, model string) {
	if snapshot == nil {
		return
	}
	if strings.TrimSpace(snapshot.Model) == "" && strings.TrimSpace(model) != "" {
		snapshot.Model = xai.ResolveGrokTextResponsesModelID(model)
	}
	var previous *xai.QuotaSnapshot
	if account != nil {
		previous, _ = grokQuotaSnapshotFromExtra(account.Extra)
	}
	snapshot.ApplyGrok45ResponsesPlanSignal(previous)
}

func grokBillingSnapshotFromExtra(extra map[string]any) (*xai.BillingSummary, error) {
	if extra == nil {
		return nil, nil
	}
	raw, ok := extra[grokBillingExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	switch snapshot := raw.(type) {
	case *xai.BillingSummary:
		return snapshot, nil
	case xai.BillingSummary:
		return &snapshot, nil
	case map[string]any:
		data, err := json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
		var out xai.BillingSummary
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal grok billing snapshot: %w", err)
		}
		var out xai.BillingSummary
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
}

func grokQuotaSnapshotFromExtra(extra map[string]any) (*xai.QuotaSnapshot, error) {
	if extra == nil {
		return nil, nil
	}
	raw, ok := extra[grokQuotaSnapshotExtraKey]
	if !ok || raw == nil {
		return nil, nil
	}
	switch snapshot := raw.(type) {
	case *xai.QuotaSnapshot:
		return snapshot, nil
	case xai.QuotaSnapshot:
		return &snapshot, nil
	case map[string]any:
		data, err := json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
		var out xai.QuotaSnapshot
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal grok quota snapshot: %w", err)
		}
		var out xai.QuotaSnapshot
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
}
