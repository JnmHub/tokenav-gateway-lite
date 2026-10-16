package service

import "time"

func (r *contentModerationAIRuntime) testStatus(fingerprint string) ContentModerationAITestStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.lastTest
	if status.Status == "" {
		return ContentModerationAITestStatus{Status: "not_tested", ConfigFingerprint: fingerprint}
	}
	if status.ConfigFingerprint != fingerprint {
		status.Status = "stale"
		r.lastTest.Status = "stale"
	}
	return status
}

func (r *contentModerationAIRuntime) beginTest(fingerprint, caseID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.testSequence++
	r.lastTest = ContentModerationAITestStatus{Status: "running", ConfigFingerprint: fingerprint, CaseID: caseID}
	return r.testSequence
}

func (r *contentModerationAIRuntime) finishTest(sequence uint64, test ContentModerationAITestStatus) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Older concurrent tests must not replace a newer result, even when the
	// provider ignores cancellation or the operator saved another configuration.
	if sequence == r.testSequence {
		if r.lastTest.Status == "stale" {
			test.Status = "stale"
		}
		r.lastTest = test
		return test.Status != "stale"
	}
	return false
}

func contentModerationAISyntheticStatus(caseID, fingerprint string, d *ContentModerationDecision) ContentModerationAITestStatus {
	now := time.Now().UTC()
	test := ContentModerationAITestStatus{Status: "failed", ConfigFingerprint: fingerprint, CaseID: caseID, Verdict: d.Verdict, ErrorCode: d.ErrorCode, ProviderHTTPStatus: d.ProviderHTTPStatus, CoverageComplete: d.Coverage.Complete, TestedAt: &now}
	if caseID == "unsupported_media" || caseID == "uncovered_field" {
		// 未覆盖案例还必须保留未覆盖证据，不能仅凭一个有效判定宣称测试通过。
		if d.ErrorCode == "" && d.Coverage.Complete && d.Verdict == "allow" && len(d.Coverage.UncoveredFields) > 0 {
			test.Status = "passed"
		} else if d.ErrorCode == "" {
			test.ErrorCode = "ai_test_unexpected_verdict"
		}
		return test
	}
	if d.ErrorCode == "" && d.Coverage.Complete && (d.Verdict == "allow" || d.Verdict == "block") {
		expected := ""
		switch caseID {
		case "allow":
			expected = "allow"
		case "block", "long_tail":
			expected = "block"
		}
		if expected != "" && d.Verdict != expected {
			test.ErrorCode = "ai_test_unexpected_verdict"
		} else {
			test.Status = "passed"
		}
	}
	return test
}

func buildContentModerationAIReadiness(cfg *ContentModerationConfig, saved, riskEnabled bool, m *ContentModerationAIMetrics) ContentModerationAIReadiness {
	readiness := ContentModerationAIReadiness{State: "production_not_ready", Ready: true}
	add := func(code string, ready bool, message string) {
		readiness.Checks = append(readiness.Checks, ContentModerationAIReadinessCheck{Code: code, Ready: ready, Message: message})
		readiness.Ready = readiness.Ready && ready
	}
	add("config_saved", saved, "先保存专用 AI 审查配置")
	add("config_valid", cfg.AI.validate() == nil, "服务地址、模型或高级配置无效，请重新保存")
	add("risk_control_enabled", riskEnabled, "风控总开关未开启")
	add("content_moderation_enabled", cfg.Enabled, "内容审查开关未开启")
	add("ai_engine", cfg.KeywordBlockingMode == ContentModerationKeywordModeAIOnly, "审查引擎尚未选择纯 AI")
	add("review_mode", cfg.Mode == ContentModerationModeShadow || cfg.Mode == ContentModerationModePreBlock, "AI 运行模式需选择观察或同步拦截")
	add("group_scope", cfg.AllGroups || len(cfg.GroupIDs) > 0, "尚未选择需要审查的分组")
	add("model_scope", cfg.ModelFilter.Type == ContentModerationModelFilterAll || len(cfg.ModelFilter.Models) > 0, "指定或排除模型的清单为空，请明确业务模型范围")
	add("dedicated_key", cfg.AI.APIKey != "", "未保存专用审查密钥；旧审核密钥不会复用")
	add("external_data_consent", cfg.AI.ExternalDataConsent, "尚未显式授权发送客户审查内容到外部服务")
	if cfg.AI.PenaltiesEnabled {
		add("penalty_store", m.StoreReady, "违规处罚存储未就绪")
	}
	// 合成测试仅是独立验证证据；过期、失败或重启丢失证据都不改变生产运行条件。
	if readiness.Ready {
		readiness.State = "production_conditions_met"
	}
	return readiness
}
