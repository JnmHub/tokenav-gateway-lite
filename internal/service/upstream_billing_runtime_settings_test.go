package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type upstreamBillingRuntimeSettingRepoStub struct {
	*panelRateLimitSettingRepoStub
}

func (s *upstreamBillingRuntimeSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func TestUpstreamBillingRuntimeSettingsDefaultClosedAndBounded(t *testing.T) {
	resetUpstreamBillingRuntimeSettingsForTest()
	t.Cleanup(resetUpstreamBillingRuntimeSettingsForTest)

	defaults := CurrentUpstreamBillingRuntimeSettings()
	require.False(t, defaults.AutoProbeEnabled)
	require.False(t, defaults.CostSchedulingEnabled)
	require.Equal(t, 30*time.Minute, defaults.ProbeInterval)
	require.Equal(t, 20, defaults.ProbeBatchSize)
	require.Equal(t, 4, defaults.ProbeConcurrency)
	require.Zero(t, defaults.UpstreamCostWeight)

	decoded := decodeUpstreamBillingRuntimeSettings(map[string]string{
		SettingKeyUpstreamBillingAutoProbeEnabled:     "true",
		SettingKeyUpstreamBillingProbeIntervalMinutes: "99999",
		SettingKeyUpstreamBillingProbeBatchSize:       "999",
		SettingKeyUpstreamBillingProbeConcurrency:     "99",
		SettingKeyOpenAIUpstreamCostWeight:            "9",
		SettingKeyOpenAIQuotaHeadroomWeight:           "NaN",
	})
	require.True(t, decoded.AutoProbeEnabled)
	require.Equal(t, 1440*time.Minute, decoded.ProbeInterval)
	require.Equal(t, 20, decoded.ProbeBatchSize)
	require.Equal(t, 4, decoded.ProbeConcurrency)
	require.Equal(t, 2.0, decoded.UpstreamCostWeight)
	require.Zero(t, decoded.QuotaHeadroomWeight)
}

func TestUpstreamBillingRuntimeSettingsWarmAndRefreshImmediately(t *testing.T) {
	resetUpstreamBillingRuntimeSettingsForTest()
	t.Cleanup(resetUpstreamBillingRuntimeSettingsForTest)
	repo := &upstreamBillingRuntimeSettingRepoStub{panelRateLimitSettingRepoStub: &panelRateLimitSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamBillingAutoProbeEnabled:     "true",
		SettingKeyUpstreamBillingProbeIntervalMinutes: "60",
		SettingKeyUpstreamBillingProbeBatchSize:       "10",
		SettingKeyUpstreamBillingProbeConcurrency:     "2",
	}}}
	svc := &SettingService{settingRepo: repo}
	require.NoError(t, svc.WarmUpstreamBillingRuntimeSettings(context.Background()))
	warmed := CurrentUpstreamBillingRuntimeSettings()
	require.True(t, warmed.AutoProbeEnabled)
	require.Equal(t, time.Hour, warmed.ProbeInterval)
	require.Equal(t, 10, warmed.ProbeBatchSize)
	require.Equal(t, 2, warmed.ProbeConcurrency)

	svc.refreshCachedSettings(&SystemSettings{
		UpstreamBillingProbeIntervalMinutes: 15,
		UpstreamBillingProbeBatchSize:       5,
		UpstreamBillingProbeConcurrency:     1,
		OpenAIUpstreamCostSchedulingEnabled: true,
		OpenAIUpstreamCostWeight:            0.5,
	})
	updated := CurrentUpstreamBillingRuntimeSettings()
	require.False(t, updated.AutoProbeEnabled)
	require.True(t, updated.CostSchedulingEnabled)
	require.Equal(t, 15*time.Minute, updated.ProbeInterval)
	require.Equal(t, 0.5, updated.UpstreamCostWeight)
}
