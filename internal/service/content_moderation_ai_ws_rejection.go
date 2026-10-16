package service

import (
	"context"
	"strings"

	coderws "github.com/coder/websocket"
)

// A rejected frame earns a create identity only after the complete, unique JSON
// object proves it is a text response.create. Never infer a turn from broken JSON.
func isOpenAIWSRejectedResponseCreate(msgType coderws.MessageType, payload []byte) bool {
	if msgType != coderws.MessageText {
		return false
	}
	parsed, err := strictJSON(payload)
	if err != nil {
		return false
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	typ, ok := root["type"].(string)
	return ok && typ == "response.create"
}

// ValidateOpenAIWSFirstClientFrame 在首轮送审前复用传输层的严格校验。
// 不支持的历史和媒体不能先外发给模型；拒绝观察器仍只认完整文本 create，
// 不为二进制、破损或重复键 JSON 伪造审查轮次，也不触碰业务资金。
func ValidateOpenAIWSFirstClientFrame(hooks *OpenAIWSIngressHooks, msgType coderws.MessageType, payload []byte, originalModel string) error {
	strict, err := openAIWSStrictClientFrames(hooks)
	if err != nil || !strict {
		return err
	}
	if err := validateOpenAIWSStrictClientFrame(msgType, payload, strict); err != nil {
		notifyOpenAIWSStrictRejection(hooks, msgType, payload, strict, originalModel)
		return err
	}
	return nil
}

// The caller has already rejected this frame. Even a broken observer
// must not turn that rejection into an upstream dispatch or replace its 1008.
func notifyOpenAIWSStrictRejection(hooks *OpenAIWSIngressHooks, msgType coderws.MessageType, payload []byte, strict bool, originalModel string) {
	if !strict || hooks == nil || hooks.OnStrictResponseCreateRejected == nil || !isOpenAIWSRejectedResponseCreate(msgType, payload) {
		return
	}
	defer func() {
		if recover() != nil {
			logOpenAIWSV2Passthrough("strict_rejection_notification_failed")
		}
	}()
	hooks.OnStrictResponseCreateRejected(append([]byte(nil), payload...), originalModel)
}

// RecordAIWSStrictRejection 只记录传输层已经作出的拒绝。输入抽取只补充
// 可复核证据，不能改变严格帧校验、调用供应商或触发处罚及业务资金操作。
func (s *ContentModerationService) RecordAIWSStrictRejection(ctx context.Context, input ContentModerationCheckInput, admit func() (string, uint64)) *ContentModerationDecision {
	if s == nil {
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503))
	}
	snapshot, err := s.loadRuntimeSnapshot(ctx)
	if err != nil || snapshot == nil || snapshot.config == nil {
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503))
	}
	cfg := snapshot.config
	// Keep the same enablement/scope contract as the existing unsupported gate.
	if !snapshot.riskControlEnabled || !cfg.Enabled || cfg.Mode == ContentModerationModeOff || cfg.KeywordBlockingMode != ContentModerationKeywordModeAIOnly || !cfg.includesGroup(input.GroupID) {
		return nil
	}
	if strings.TrimSpace(input.Model) != "" && !cfg.includesModel(input.Model) {
		return nil
	}
	// Allocate exactly once, only after the same snapshot proves enabled scope.
	// Off/out-of-scope notifications must not consume a connection's AI turn.
	if admit != nil {
		input.AdmissionID, input.Turn = admit()
	}
	if input.AdmissionID == "" || input.Turn == 0 {
		return contentModerationAIFailure(aiError("ai_missing_admission", 503))
	}
	acfg := cfg.AI
	acfg.normalize()
	canonical, classifyErr := canonicalContentModerationAIInput(input.Protocol, input.Body, acfg, true)
	if classifyErr == nil {
		// A text payload may be canonical yet lack the stricter funding proof.
		// Classification cannot override a rejection already made by the transport.
		classifyErr = aiError("ai_strict_frame_rejected", 503)
	}
	decision := contentModerationAIFailure(classifyErr)
	decision.Phase = "preparation"
	decision.Reason = "传输层严格帧校验已拒绝本轮，未调用审查模型"
	if canonical != nil {
		decision.InputHash = canonical.InputHash
		decision.Coverage = contentModerationAICoverage(canonical, false)
	}
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
		input.RequestID = ContentModerationAdmissionTurnID(input.AdmissionID, input.Turn)
		text := redactContentModerationAIText(buildAIReviewTextExcerpt(canonical), acfg.APIKey)
		input.Body = nil // 队列仅保存脱敏提取证据，不保留原始帧及图片数据。
		log := s.buildLog(input, cfg, ContentModerationActionError, false, "", 0, nil, text, nil, nil, decision.ErrorCode)
		log.FullInputText = text
		log.AIReviewDetail = s.buildAIReviewDetail(canonical, decision)
		log.AIReviewDetail["model"] = acfg.Model
		log.AIReviewDetail["adapter"] = acfg.Adapter
		log.AIReviewDetail["policy_version"] = acfg.PolicyVersion
		s.enqueueRecord(ctx, input, cfg, log, "", false, false)
	}
	return decision
}
