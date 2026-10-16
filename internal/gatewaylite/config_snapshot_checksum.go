package gatewaylite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

type configSnapshotPolicyChecksum struct {
	LocalGroupID int64  `json:"local_group_id"`
	Checksum     string `json:"checksum"`
}

type configSnapshotChecksumPayload struct {
	Accounts        []GatewayAccountSnapshot       `json:"accounts"`
	Groups          []GatewayGroupSnapshot         `json:"groups"`
	ModelPrices     []GatewayModelPrice            `json:"model_prices"`
	Policies        []configSnapshotPolicyChecksum `json:"policies"`
	RuntimeSettings *GatewayRuntimeSettings        `json:"runtime_settings,omitempty"`
}

// GatewayConfigPushSnapshotChecksum 只计算配置的逻辑内容，不包含每次构建都会变化的
// generated/received timestamp（生成/接收时间）。因此周期保活不会伪装成配置变更；
// 账号、分组、价格、路由或运行开关发生真实变化时仍会得到新的校验和。
func GatewayConfigPushSnapshotChecksum(snapshot GatewayConfigPushSnapshot) (string, error) {
	payload := configSnapshotChecksumPayload{
		Accounts:    append([]GatewayAccountSnapshot(nil), snapshot.Accounts...),
		Groups:      cloneGatewayConfigChecksumGroups(snapshot.Groups),
		ModelPrices: cloneGatewayConfigChecksumPrices(snapshot.ModelPrices),
		Policies:    make([]configSnapshotPolicyChecksum, 0, len(snapshot.EffectivePolicies)),
	}
	for index := range payload.Groups {
		payload.Groups[index].EffectiveModelsVersion = 0
		sortGatewayConfigChecksumPrices(payload.Groups[index].ModelPrices)
	}
	for _, policy := range snapshot.EffectivePolicies {
		payload.Policies = append(payload.Policies, configSnapshotPolicyChecksum{
			LocalGroupID: policy.LocalGroupID,
			Checksum:     strings.ToLower(strings.TrimSpace(policy.Checksum)),
		})
	}
	if snapshot.RuntimeSettings != nil {
		copySettings := *snapshot.RuntimeSettings
		if snapshot.RuntimeSettings.OpenAIFastPolicySettings != nil {
			copyFastPolicy := *snapshot.RuntimeSettings.OpenAIFastPolicySettings
			copyFastPolicy.ReportedAtMillis = 0
			copySettings.OpenAIFastPolicySettings = &copyFastPolicy
		}
		payload.RuntimeSettings = &copySettings
	}
	sort.Slice(payload.Accounts, func(i, j int) bool { return payload.Accounts[i].ID < payload.Accounts[j].ID })
	sort.Slice(payload.Groups, func(i, j int) bool { return payload.Groups[i].ID < payload.Groups[j].ID })
	sortGatewayConfigChecksumPrices(payload.ModelPrices)
	sort.Slice(payload.Policies, func(i, j int) bool { return payload.Policies[i].LocalGroupID < payload.Policies[j].LocalGroupID })
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneGatewayConfigChecksumGroups(groups []GatewayGroupSnapshot) []GatewayGroupSnapshot {
	out := append([]GatewayGroupSnapshot(nil), groups...)
	for index := range out {
		out[index].EffectiveModels = append([]string(nil), groups[index].EffectiveModels...)
		out[index].ModelPrices = cloneGatewayConfigChecksumPrices(groups[index].ModelPrices)
	}
	return out
}

func cloneGatewayConfigChecksumPrices(prices []GatewayModelPrice) []GatewayModelPrice {
	out := append([]GatewayModelPrice(nil), prices...)
	for index := range out {
		out[index].UpdatedAtMillis = 0
		out[index].Intervals = append([]GatewayModelPriceInterval(nil), prices[index].Intervals...)
		sort.Slice(out[index].Intervals, func(i, j int) bool {
			left, right := out[index].Intervals[i], out[index].Intervals[j]
			if left.SortOrder != right.SortOrder {
				return left.SortOrder < right.SortOrder
			}
			if left.TierLabel != right.TierLabel {
				return strings.ToLower(strings.TrimSpace(left.TierLabel)) < strings.ToLower(strings.TrimSpace(right.TierLabel))
			}
			return left.MinTokens < right.MinTokens
		})
	}
	return out
}

func sortGatewayConfigChecksumPrices(prices []GatewayModelPrice) {
	sort.Slice(prices, func(i, j int) bool {
		leftModel := strings.ToLower(strings.TrimSpace(prices[i].Model))
		rightModel := strings.ToLower(strings.TrimSpace(prices[j].Model))
		if leftModel != rightModel {
			return leftModel < rightModel
		}
		return strings.ToLower(strings.TrimSpace(prices[i].Provider)) < strings.ToLower(strings.TrimSpace(prices[j].Provider))
	})
}
