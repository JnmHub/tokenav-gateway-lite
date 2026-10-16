package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// [官方 v0.2.13 移植] cyber 用户白名单的解析与运行时缓存护栏。

// cyberAllowlistSettingRepo 是 map 型设置仓库（只实现读取路径）。
type cyberAllowlistSettingRepo struct {
	values map[string]string
}

func (r *cyberAllowlistSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *cyberAllowlistSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (r *cyberAllowlistSettingRepo) Set(context.Context, string, string) error { return nil }

func (r *cyberAllowlistSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *cyberAllowlistSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }

func (r *cyberAllowlistSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

func (r *cyberAllowlistSettingRepo) Delete(context.Context, string) error { return nil }

func TestParseCyberPolicyUserAllowlist(t *testing.T) {
	t.Run("逗号与空白都接受", func(t *testing.T) {
		ids, err := ParseCyberPolicyUserAllowlist("12,34\n 56\t78")
		require.NoError(t, err)
		require.Len(t, ids, 4)
		for _, id := range []int64{12, 34, 56, 78} {
			_, ok := ids[id]
			require.True(t, ok, "应包含用户 %d", id)
		}
	})

	t.Run("空串得到空集合", func(t *testing.T) {
		ids, err := ParseCyberPolicyUserAllowlist("   ")
		require.NoError(t, err)
		require.Empty(t, ids)
	})

	t.Run("任一非法项整表拒绝", func(t *testing.T) {
		for _, raw := range []string{"12,abc", "0", "-5", "12 34 x", "1e3", "１２"} {
			_, err := ParseCyberPolicyUserAllowlist(raw)
			require.Error(t, err, "raw=%q 必须整表拒绝", raw)
		}
	})

	t.Run("多余分隔符不算非法（与官方 FieldsFunc 语义一致）", func(t *testing.T) {
		ids, err := ParseCyberPolicyUserAllowlist("12,,")
		require.NoError(t, err)
		require.Len(t, ids, 1)
		_, ok := ids[12]
		require.True(t, ok)
	})

	t.Run("超长输入拒绝", func(t *testing.T) {
		_, err := ParseCyberPolicyUserAllowlist(strings.Repeat("1", 16385))
		require.Error(t, err)
	})
}

func TestIsCyberPolicyUserAllowlistedFromRuntimeCache(t *testing.T) {
	settings := NewSettingService(&cyberAllowlistSettingRepo{values: map[string]string{
		SettingKeyCyberPolicyUserAllowlist: "12, 34",
	}}, &config.Config{})
	ctx := context.Background()

	require.True(t, settings.IsCyberPolicyUserAllowlisted(ctx, 12))
	require.True(t, settings.IsCyberPolicyUserAllowlisted(ctx, 34))
	require.False(t, settings.IsCyberPolicyUserAllowlisted(ctx, 56))
	require.False(t, settings.IsCyberPolicyUserAllowlisted(ctx, 0), "非法用户 ID 一律不放行")
}

func TestIsCyberPolicyUserAllowlistedFailsClosedOnInvalidConfig(t *testing.T) {
	settings := NewSettingService(&cyberAllowlistSettingRepo{values: map[string]string{
		SettingKeyCyberPolicyUserAllowlist: "12,not-a-number",
	}}, &config.Config{})

	// 整表非法 → 不豁免任何用户。绝不能因为「解析到一半」把 12 放行。
	require.False(t, settings.IsCyberPolicyUserAllowlisted(context.Background(), 12))
}

func TestCyberPolicyLogOnlyFollowsAllowlist(t *testing.T) {
	settings := NewSettingService(&cyberAllowlistSettingRepo{values: map[string]string{
		SettingKeyCyberPolicyUserAllowlist: "12",
	}}, &config.Config{})
	svc := &OpenAIGatewayService{settingService: settings}
	ctx := context.Background()

	require.True(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 1, UserID: 12}))
	require.False(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 2, UserID: 13}))
	require.False(t, svc.CyberPolicyLogOnly(ctx, nil))
}

// 无白名单配置时行为完全不变（默认关闭），避免默认部署被这次移植影响。
func TestCyberPolicyAllowlistDefaultsToEmpty(t *testing.T) {
	settings := NewSettingService(&cyberAllowlistSettingRepo{values: map[string]string{}}, &config.Config{})
	svc := &OpenAIGatewayService{settingService: settings}
	ctx := context.Background()

	require.False(t, settings.IsCyberPolicyUserAllowlisted(ctx, 12))
	require.False(t, svc.CyberPolicyLogOnly(ctx, &APIKey{ID: 1, UserID: 12}))
}

// 风控运行时快照必须带上白名单：命中 cyber_policy 时按 log-only 处理（不封号）。
func TestCyberAllowlistReachesContentModerationSnapshot(t *testing.T) {
	settings := &cyberAllowlistSettingRepo{values: map[string]string{
		SettingKeyCyberPolicyUserAllowlist: "12",
		SettingKeyRiskControlEnabled:       "true",
	}}
	svc := NewContentModerationService(settings, nil, nil, nil, nil, nil, nil)
	require.NotNil(t, svc)

	snapshot, err := svc.loadRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	require.NotNil(t, snapshot)

	require.True(t, snapshot.isUserAllowlisted(12))
	require.False(t, snapshot.isUserAllowlisted(13))
	require.False(t, snapshot.isUserAllowlisted(0), "非法用户 ID 一律不豁免")
	require.True(t, snapshot.riskControlEnabled)
}

// 快照决策的边界：nil 快照/空白名单都不豁免（fail-closed）。
func TestContentModerationSnapshotAllowlistDecision(t *testing.T) {
	var nilSnapshot *contentModerationRuntimeSnapshot
	require.False(t, nilSnapshot.isUserAllowlisted(12))

	empty := &contentModerationRuntimeSnapshot{}
	require.False(t, empty.isUserAllowlisted(12))

	withList := &contentModerationRuntimeSnapshot{allowlistedUsers: map[int64]struct{}{12: {}}}
	require.True(t, withList.isUserAllowlisted(12))
	require.False(t, withList.isUserAllowlisted(34))
}

// 后台设置更新入口（GET/PUT /api/v1/admin/settings 的落库前校验）必须拒绝非法白名单，
// 否则会出现「运营以为加进去了、实际整表没生效」这种最难排查的状态。
func TestBuildSystemSettingsUpdatesValidatesCyberAllowlist(t *testing.T) {
	settings := NewSettingService(&cyberAllowlistSettingRepo{values: map[string]string{}}, &config.Config{})
	ctx := context.Background()

	_, err := settings.buildSystemSettingsUpdates(ctx, &SystemSettings{CyberPolicyUserAllowlist: "12,not-a-number"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "INVALID_CYBER_POLICY_USER_ALLOWLIST")

	updates, err := settings.buildSystemSettingsUpdates(ctx, &SystemSettings{CyberPolicyUserAllowlist: " 12, 34 "})
	require.NoError(t, err)
	require.Equal(t, "12, 34", updates[SettingKeyCyberPolicyUserAllowlist], "写入前应去掉首尾空白")

	updates, err = settings.buildSystemSettingsUpdates(ctx, &SystemSettings{CyberPolicyUserAllowlist: ""})
	require.NoError(t, err)
	require.Equal(t, "", updates[SettingKeyCyberPolicyUserAllowlist], "空串必须能清空白名单")
}
