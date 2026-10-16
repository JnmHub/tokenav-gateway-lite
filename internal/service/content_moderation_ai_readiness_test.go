package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentModerationAIReadinessIndependentOfTestEvidence(t *testing.T) {
	cfg := aiTestConfig("http://review.internal", ContentModerationAIAdapterChat)
	s, _ := aiReviewService(t, cfg)
	fingerprint := contentModerationAIConfigFingerprint(cfg)
	for _, status := range []string{"not_tested", "running", "passed", "failed", "stale"} {
		t.Run(status, func(t *testing.T) {
			r := s.aiRuntime()
			r.mu.Lock()
			r.lastTest = ContentModerationAITestStatus{Status: status, ConfigFingerprint: fingerprint}
			r.mu.Unlock()
			m, err := s.GetAIMetrics(context.Background())
			require.NoError(t, err)
			require.Equal(t, status, m.Test.Status)
			require.True(t, m.Production.Ready)
			require.Equal(t, "production_conditions_met", m.Production.State)
			require.Empty(t, aiReviewMissingChecks(m))
			for _, check := range m.Production.Checks {
				require.NotEqual(t, "saved_config_tested", check.Code)
			}
			require.Zero(t, m.TotalCalls, "reading readiness must not run a synthetic test")
		})
	}
}

func TestContentModerationAIReadinessStillRequiresRuntimeConditions(t *testing.T) {
	for _, tc := range []struct {
		code   string
		mutate func(*ContentModerationConfig)
	}{
		{"risk_control_enabled", func(*ContentModerationConfig) {}},
		{"content_moderation_enabled", func(c *ContentModerationConfig) { c.Enabled = false }},
		{"review_mode", func(c *ContentModerationConfig) { c.Mode = ContentModerationModeOff }},
		{"dedicated_key", func(c *ContentModerationConfig) { c.AI.APIKey = "" }},
		{"external_data_consent", func(c *ContentModerationConfig) { c.AI.ExternalDataConsent = false }},
		{"penalty_store", func(c *ContentModerationConfig) { c.AI.PenaltiesEnabled = true }},
	} {
		t.Run(tc.code, func(t *testing.T) {
			cfg := aiTestConfig("http://review.internal", ContentModerationAIAdapterChat)
			tc.mutate(cfg)
			s, settings := aiReviewService(t, cfg)
			s.SetAIStore(nil)
			if tc.code == "risk_control_enabled" {
				require.NoError(t, settings.SetMultiple(context.Background(), map[string]string{SettingKeyRiskControlEnabled: "false"}))
			}
			fingerprint := contentModerationAIConfigFingerprint(cfg)
			for _, status := range []string{"not_tested", "running", "passed", "failed", "stale"} {
				r := s.aiRuntime()
				r.mu.Lock()
				r.lastTest = ContentModerationAITestStatus{Status: status, ConfigFingerprint: fingerprint}
				r.mu.Unlock()
				m, err := s.GetAIMetrics(context.Background())
				require.NoError(t, err)
				require.False(t, m.Production.Ready, status)
				require.Equal(t, "production_not_ready", m.Production.State)
				require.Equal(t, []string{tc.code}, aiReviewMissingChecks(m), status)
				require.Equal(t, status, m.Test.Status)
				require.Zero(t, m.TotalCalls)
			}
		})
	}
}

func TestContentModerationAICheckWithoutCurrentSyntheticEvidence(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		t.Run(adapter, func(t *testing.T) {
			provider, calls, _ := aiSyntheticProvider(t, adapter, nil, 200, 0)
			defer provider.Close()
			cfg := aiTestConfig(provider.URL, adapter)
			s, settings := aiReviewService(t, cfg)
			// 构造后注入日志仓储，避免启动无关后台任务；配置仍经正式保存、读取及 Check 入口。
			s.repo = &aiLogRepo{}
			ctx := context.Background()
			input := aiTestInput(`{"input":"safe production text"}`)
			assertCheck := func(svc *ContentModerationService, status string) {
				t.Helper()
				before, err := svc.GetAIMetrics(ctx)
				require.NoError(t, err)
				require.Equal(t, status, before.Test.Status)
				require.True(t, before.Production.Ready)
				beforeCalls := calls.Load()
				input.Turn++
				d, err := svc.Check(ctx, input)
				require.NoError(t, err)
				require.Equal(t, "allow", d.Verdict)
				require.True(t, d.Allowed)
				require.True(t, d.ProviderCalled)
				require.True(t, d.Coverage.Complete)
				require.Equal(t, "completed", d.Phase)
				require.Equal(t, beforeCalls+1, calls.Load(), "each production Check calls the provider exactly once")
				after, err := svc.GetAIMetrics(ctx)
				require.NoError(t, err)
				require.Equal(t, before.Test, after.Test, "a production result cannot become synthetic evidence")
				require.Equal(t, before.ProductionChecked+1, after.ProductionChecked)
				require.Equal(t, before.ProductionAllowed+1, after.ProductionAllowed)
			}
			assertCheck(s, "not_tested")
			tested, err := s.TestAI(ctx, TestContentModerationAIInput{CaseID: "allow"})
			require.NoError(t, err)
			require.Equal(t, "passed", tested.Test.Status)
			beforeSave := calls.Load()
			imageReview := false
			view, err := s.UpdateConfig(ctx, UpdateContentModerationConfigInput{AI: &UpdateContentModerationAIConfigInput{ImageReviewEnabled: &imageReview}})
			require.NoError(t, err)
			require.NotEqual(t, tested.ConfigFingerprint, view.AI.ConfigFingerprint)
			require.False(t, view.AI.ImageReviewEnabled)
			reloaded, err := s.GetConfig(ctx)
			require.NoError(t, err)
			require.Equal(t, view.AI.ConfigFingerprint, reloaded.AI.ConfigFingerprint)
			require.False(t, reloaded.AI.ImageReviewEnabled)
			m, err := s.GetAIMetrics(ctx)
			require.NoError(t, err)
			require.Equal(t, "stale", m.Test.Status)
			require.True(t, m.Production.Ready)
			require.Equal(t, beforeSave, calls.Load(), "saving and rereading must not call the provider")
			assertCheck(s, "stale")

			// 新服务只共享持久配置，证明重启丢失进程内证据不增加生产门槛。
			restarted := NewContentModerationService(settings, nil, nil, nil, nil, nil, nil)
			restarted.repo = &aiLogRepo{}
			assertCheck(restarted, "not_tested")
			require.EqualValues(t, 4, calls.Load(), "three production requests plus one explicitly requested test")
		})
	}
}
