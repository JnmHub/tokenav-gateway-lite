package gatewaylite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

const EffectivePolicySchemaVersion = 1

const (
	EffectivePolicyStateAvailable   = "available"
	EffectivePolicyStateDisabled    = "disabled"
	EffectivePolicyStateUnavailable = "unavailable"
	EffectivePolicyStateUnsupported = "unsupported"
	EffectivePolicyStateStale       = "stale"
	EffectivePolicyStateUnknown     = "unknown"
)

// GatewayEffectivePolicy is a read-only shadow result. It is calculated from
// the same local account/group/model/price facts used by the gateway, but it
// never participates in the request hot path. The control plane may display it
// and compare revisions, but cannot use the mirror to widen runtime access.
type GatewayEffectivePolicy struct {
	SchemaVersion               int                           `json:"schema_version"`
	Revision                    int64                         `json:"revision"`
	Checksum                    string                        `json:"checksum"`
	GeneratedAtMillis           int64                         `json:"generated_at_millis"`
	ReceivedAtMillis            int64                         `json:"received_at_millis,omitempty"`
	Source                      string                        `json:"source"`
	GatewayNodeID               int64                         `json:"gateway_node_id,omitempty"`
	GatewayCode                 string                        `json:"gateway_code,omitempty"`
	GroupID                     int64                         `json:"group_id,omitempty"`
	LocalGroupID                int64                         `json:"local_group_id"`
	GroupName                   string                        `json:"group_name"`
	Platform                    string                        `json:"platform"`
	MinimumTier                 *int                          `json:"minimum_tier"`
	EffectiveRequiredTier       int                           `json:"effective_required_tier"`
	State                       string                        `json:"state"`
	ReasonCode                  string                        `json:"reason_code"`
	AllowBlockedKeywordRequests bool                          `json:"allow_blocked_keyword_requests"`
	AllowLive                   bool                          `json:"allow_live"`
	BillingModelSource          string                        `json:"billing_model_source,omitempty"`
	MaxReasoningEffort          string                        `json:"max_reasoning_effort,omitempty"`
	ReasoningEffortMappings     []ReasoningEffortMapping      `json:"reasoning_effort_mappings,omitempty"`
	EligibleAccountCount        int                           `json:"eligible_account_count"`
	EffectiveModelCount         int                           `json:"effective_model_count"`
	Capabilities                []EffectivePolicyCapability   `json:"capabilities"`
	Models                      []EffectivePolicyModel        `json:"models"`
	SourceVersions              EffectivePolicySourceVersions `json:"source_versions"`
	Stale                       bool                          `json:"stale,omitempty"`
}

type EffectivePolicySourceVersions struct {
	Groups          int64 `json:"groups"`
	Accounts        int64 `json:"accounts"`
	Prices          int64 `json:"prices"`
	Compatibility   int64 `json:"compatibility"`
	CompositeRoutes int64 `json:"composite_routes,omitempty"`
}

type EffectivePolicyCapability struct {
	ID                   string `json:"id"`
	State                string `json:"state"`
	ReasonCode           string `json:"reason_code"`
	EligibleAccountCount int    `json:"eligible_account_count"`
	PriceComplete        bool   `json:"price_complete"`
	Executor             string `json:"executor"`
}

type EffectivePolicyModel struct {
	Model                string                      `json:"model"`
	TargetPlatform       string                      `json:"target_platform,omitempty"`
	UpstreamModel        string                      `json:"upstream_model,omitempty"`
	PriceModel           string                      `json:"price_model,omitempty"`
	PriceSource          string                      `json:"price_source,omitempty"`
	RouteSource          string                      `json:"route_source,omitempty"`
	EligibleAccountCount int                         `json:"eligible_account_count,omitempty"`
	BillingMode          string                      `json:"billing_mode"`
	State                string                      `json:"state"`
	ReasonCode           string                      `json:"reason_code"`
	Tiers                []EffectivePolicyCapability `json:"tiers"`
}

// FinalizeEffectivePolicy normalizes ordering before calculating checksum, so
// identical logical inputs produce identical hashes across retries.
func FinalizeEffectivePolicy(policy GatewayEffectivePolicy) (GatewayEffectivePolicy, error) {
	policy = NormalizeEffectivePolicy(policy)
	checksum, err := EffectivePolicyChecksum(policy)
	if err != nil {
		return GatewayEffectivePolicy{}, err
	}
	policy.Checksum = checksum
	return policy, nil
}

func NormalizeEffectivePolicy(policy GatewayEffectivePolicy) GatewayEffectivePolicy {
	if policy.SchemaVersion <= 0 {
		policy.SchemaVersion = EffectivePolicySchemaVersion
	}
	policy.Source = strings.TrimSpace(policy.Source)
	policy.GatewayCode = strings.TrimSpace(policy.GatewayCode)
	policy.GroupName = strings.TrimSpace(policy.GroupName)
	policy.Platform = strings.ToLower(strings.TrimSpace(policy.Platform))
	policy.BillingModelSource = strings.ToLower(strings.TrimSpace(policy.BillingModelSource))
	policy.MaxReasoningEffort = strings.ToLower(strings.TrimSpace(policy.MaxReasoningEffort))
	for i := range policy.ReasoningEffortMappings {
		policy.ReasoningEffortMappings[i].From = strings.ToLower(strings.TrimSpace(policy.ReasoningEffortMappings[i].From))
		policy.ReasoningEffortMappings[i].To = strings.ToLower(strings.TrimSpace(policy.ReasoningEffortMappings[i].To))
	}
	sort.Slice(policy.ReasoningEffortMappings, func(i, j int) bool {
		if policy.ReasoningEffortMappings[i].From == policy.ReasoningEffortMappings[j].From {
			return policy.ReasoningEffortMappings[i].To < policy.ReasoningEffortMappings[j].To
		}
		return policy.ReasoningEffortMappings[i].From < policy.ReasoningEffortMappings[j].From
	})
	policy.State = normalizeEffectivePolicyState(policy.State)
	policy.ReasonCode = normalizeEffectivePolicyReason(policy.ReasonCode)
	for i := range policy.Capabilities {
		policy.Capabilities[i] = normalizeEffectivePolicyCapability(policy.Capabilities[i])
	}
	sort.Slice(policy.Capabilities, func(i, j int) bool {
		return policy.Capabilities[i].ID < policy.Capabilities[j].ID
	})
	for i := range policy.Models {
		model := &policy.Models[i]
		model.Model = strings.TrimSpace(model.Model)
		model.TargetPlatform = strings.ToLower(strings.TrimSpace(model.TargetPlatform))
		model.UpstreamModel = strings.TrimSpace(model.UpstreamModel)
		model.PriceModel = strings.TrimSpace(model.PriceModel)
		model.PriceSource = strings.TrimSpace(model.PriceSource)
		model.RouteSource = strings.ToLower(strings.TrimSpace(model.RouteSource))
		model.BillingMode = strings.ToLower(strings.TrimSpace(model.BillingMode))
		model.State = normalizeEffectivePolicyState(model.State)
		model.ReasonCode = normalizeEffectivePolicyReason(model.ReasonCode)
		for tierIndex := range model.Tiers {
			model.Tiers[tierIndex] = normalizeEffectivePolicyCapability(model.Tiers[tierIndex])
		}
		sort.Slice(model.Tiers, func(i, j int) bool { return model.Tiers[i].ID < model.Tiers[j].ID })
	}
	sort.Slice(policy.Models, func(i, j int) bool { return policy.Models[i].Model < policy.Models[j].Model })
	if policy.Capabilities == nil {
		policy.Capabilities = []EffectivePolicyCapability{}
	}
	if policy.Models == nil {
		policy.Models = []EffectivePolicyModel{}
	}
	policy.EffectiveModelCount = len(policy.Models)
	return policy
}

func EffectivePolicyChecksum(policy GatewayEffectivePolicy) (string, error) {
	canonical := NormalizeEffectivePolicy(policy)
	// Transport identity and timestamps are deliberately excluded. Revision can
	// advance after a retry while the logical strategy remains unchanged.
	canonical.Checksum = ""
	canonical.Revision = 0
	canonical.GeneratedAtMillis = 0
	canonical.ReceivedAtMillis = 0
	canonical.GatewayNodeID = 0
	canonical.GatewayCode = ""
	canonical.GroupID = 0
	canonical.Stale = false
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeEffectivePolicyCapability(capability EffectivePolicyCapability) EffectivePolicyCapability {
	capability.ID = strings.ToLower(strings.TrimSpace(capability.ID))
	capability.State = normalizeEffectivePolicyState(capability.State)
	capability.ReasonCode = normalizeEffectivePolicyReason(capability.ReasonCode)
	capability.Executor = strings.ToLower(strings.TrimSpace(capability.Executor))
	if capability.Executor == "" {
		capability.Executor = "none"
	}
	return capability
}

func normalizeEffectivePolicyState(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case EffectivePolicyStateAvailable, EffectivePolicyStateDisabled, EffectivePolicyStateUnavailable,
		EffectivePolicyStateUnsupported, EffectivePolicyStateStale, EffectivePolicyStateUnknown:
		return normalized
	default:
		return EffectivePolicyStateUnknown
	}
}

func normalizeEffectivePolicyReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	return value
}
