//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 回归：鉴权快照往返必须保留分组智能分流与兜底模型配置。
// 历史缺陷：APIKeyAuthGroupSnapshot 漏掉 WeightedModelMapping 时，
// 鉴权缓存命中路径注入 ctx 的 group 不带分流配置，选号阶段
// resolveGroupModelForSelection 看到空映射直接放行客户端模型，
// 公开模型名原样发给上游。
func TestSnapshotToAPIKey_PreservesGroupWeightedMapping(t *testing.T) {
	svc := &APIKeyService{}
	groupID := int64(3)
	snapshot := &APIKeyAuthSnapshot{
		APIKeyID: 1,
		UserID:   2,
		GroupID:  &groupID,
		Group: &APIKeyAuthGroupSnapshot{
			ID:           3,
			Name:         "split-group",
			Platform:     PlatformOpenAI,
			Status:       StatusActive,
			ModelMapping: map[string]string{"gpt-single": "gpt-target"},
			WeightedModelMapping: map[string][]WeightedModelTarget{
				"gpt-split": {
					{Model: "gpt-target-a", Weight: 70},
					{Model: "gpt-target-b", Weight: 30},
				},
			},
			FallbackModel: "gpt-fallback",
		},
	}

	apiKey := svc.snapshotToAPIKey("sk-test", snapshot)
	require.NotNil(t, apiKey)
	require.NotNil(t, apiKey.Group)

	// 单目标映射（原有行为不应回退）
	require.Equal(t, map[string]string{"gpt-single": "gpt-target"}, apiKey.Group.ModelMapping)

	// 智能分流映射必须随快照还原
	require.Equal(t, map[string][]WeightedModelTarget{
		"gpt-split": {
			{Model: "gpt-target-a", Weight: 70},
			{Model: "gpt-target-b", Weight: 30},
		},
	}, apiKey.Group.WeightedModelMapping)

	// 选号兜底模型同样必须保留
	require.Equal(t, "gpt-fallback", apiKey.Group.FallbackModel)
}
