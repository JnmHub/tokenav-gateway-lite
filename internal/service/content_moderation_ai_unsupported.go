package service

import (
	"context"
	"strings"
)

// CheckAIUnsupported protects authenticated endpoints that have no fixed text
// and image extractor yet. Unknown media inside a supported protocol is handled
// by its extractor; an unintegrated protocol must not claim a successful review
// from an empty handshake. No request body is read or sent to a provider here.
func (s *ContentModerationService) CheckAIUnsupported(ctx context.Context, input ContentModerationCheckInput) *ContentModerationDecision {
	if s == nil {
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503))
	}
	snapshot, err := s.loadRuntimeSnapshot(ctx)
	if err != nil || snapshot == nil || snapshot.config == nil {
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503))
	}
	cfg := snapshot.config
	if !snapshot.riskControlEnabled || !cfg.Enabled || cfg.Mode == ContentModerationModeOff || cfg.KeywordBlockingMode != ContentModerationKeywordModeAIOnly || !cfg.includesGroup(input.GroupID) {
		return nil
	}
	// Unsupported protocols have no safely parsed model at this stage. Unknown
	// models stay in scope so an include filter cannot accidentally bypass the
	// gate; a model already frozen by admission may still prove it is out of scope.
	if strings.TrimSpace(input.Model) != "" && !cfg.includesModel(input.Model) {
		return nil
	}
	decision := contentModerationAIFailure(aiError("ai_unsupported_protocol", 503))
	decision.Reason = "当前端点尚未接入固定文本与图片抽取，原始输入未送审"
	decision.Phase = "preparation"
	decision.Categories = []string{}
	decision.Coverage.UncoveredFields = []string{"protocol:" + strings.TrimSpace(input.Protocol)}
	decision.Shadow = cfg.Mode == ContentModerationModeShadow
	r := s.aiRuntime()
	r.mu.Lock()
	r.checked++
	r.errors++
	r.productionChecked++
	r.productionErrors++
	r.lastError = decision.ErrorCode
	if decision.Shadow {
		r.shadow++
	}
	r.mu.Unlock()
	if s.repo != nil {
		log := s.buildLog(input, cfg, ContentModerationActionError, false, "", 0, nil, "", nil, nil, decision.ErrorCode)
		// No text or media has been extracted on this route. Persist only the
		// protocol boundary so administrators cannot mistake this for a review.
		log.AIReviewDetail = map[string]any{
			"verdict": decision.Verdict, "error_code": decision.ErrorCode,
			"reason": decision.Reason, "categories": decision.Categories, "phase": decision.Phase,
			"provider_called": decision.ProviderCalled, "provider_latency_ms": decision.ProviderLatencyMS,
			"coverage": decision.Coverage, "uncovered_fields": decision.Coverage.UncoveredFields,
		}
		s.enqueueRecord(ctx, input, cfg, log, "", false, false)
	}
	return decision
}
