package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_ws_forwarder_support.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"net/http"
	"strings"
)


func normalizeOpenAIWSTerminalEvent(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "response.completed":
		return "response.completed"
	case "response.done":
		return "response.done"
	case "response.failed":
		return "response.failed"
	case "response.incomplete":
		return "response.incomplete"
	case "response.cancelled", "response.canceled":
		return "response.cancelled"
	default:
		return ""
	}
}

// markOpenAIWSClientVisibleFailure records only terminal/error protocol events
// that were delivered to the client. Callers invoke it only after any hidden
// failover/recovery decision and a successful downstream write.
func markOpenAIWSClientVisibleFailure(c *gin.Context, eventType string, payload []byte) {
	eventType = strings.TrimSpace(eventType)
	if eventType != "error" && eventType != "response.failed" {
		return
	}
	prefix := "error"
	if eventType == "response.failed" {
		prefix = "response.error"
	}
	code := strings.TrimSpace(gjson.GetBytes(payload, prefix+".code").String())
	errType := strings.TrimSpace(gjson.GetBytes(payload, prefix+".type").String())
	message := strings.TrimSpace(gjson.GetBytes(payload, prefix+".message").String())
	if eventType == "response.failed" && code == "" && errType == "" && message == "" {
		prefix = "error"
		code = strings.TrimSpace(gjson.GetBytes(payload, prefix+".code").String())
		errType = strings.TrimSpace(gjson.GetBytes(payload, prefix+".type").String())
		message = strings.TrimSpace(gjson.GetBytes(payload, prefix+".message").String())
	}
	status := int(gjson.GetBytes(payload, prefix+".status_code").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, prefix+".status").Int())
	}
	if status == 0 && eventType == "error" {
		status = int(gjson.GetBytes(payload, "status").Int())
	}
	if status == 0 {
		status = openAIWSErrorHTTPStatusFromRaw(code, errType)
	}
	if errType == "" {
		errType = "upstream_error"
	}
	if code == "" {
		code = strings.ReplaceAll(eventType, ".", "_")
	}
	if message == "" {
		message = "upstream websocket request failed"
	}
	MarkOpsStreamFailure(c, errType, code, message, status)
}

func (s *OpenAIGatewayService) handleOpenAIWSTerminalTransientFailure(ctx context.Context, account *Account, canonicalModel string, headers http.Header, payload []byte) string {
	eventType, _, _ := parseOpenAIWSEventEnvelope(payload)
	terminalEvent := normalizeOpenAIWSTerminalEvent(eventType)
	if terminalEvent != "response.failed" {
		return terminalEvent
	}
	s.handleOpenAIWSFailureAccountSideEffects(ctx, account, canonicalModel, headers, payload)
	return terminalEvent
}

func (s *OpenAIGatewayService) handleOpenAIWSErrorEventTransientFailure(ctx context.Context, account *Account, canonicalModel string, headers http.Header, payload []byte) {
	eventType, _, _ := parseOpenAIWSEventEnvelope(payload)
	if eventType != "error" {
		return
	}
	status := openAIWSPayloadTransientStatus(payload)
	if status != 0 {
		if status == http.StatusTooManyRequests {
			headers = openAIWSSemantic429Headers(account, canonicalModel, headers)
		}
		s.handleOpenAIAccountUpstreamError(ctx, account, status, headers, payload, canonicalModel)
	}
}

// handleOpenAIWSFailureAccountSideEffects applies both structured credential
// failures and transient failures. Its return value lets stream callers avoid
// applying the same transition twice for an error/response.failed pair.
func (s *OpenAIGatewayService) handleOpenAIWSFailureAccountSideEffects(ctx context.Context, account *Account, canonicalModel string, headers http.Header, payload []byte) bool {
	message := extractOpenAISSEErrorMessage(payload)
	status := openAIStreamFailureStatus(payload, message)
	switch status {
	case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
		s.handleOpenAIStreamTerminalAccountSideEffects(nil, account, payload, message, headers, canonicalModel)
		return true
	case http.StatusForbidden:
		if !openAIStream403AccountFailure(payload, message) {
			return false
		}
		s.handleOpenAIStreamTerminalAccountSideEffects(nil, account, payload, message, headers, canonicalModel)
		return true
	}

	status = openAIWSPayloadTransientStatus(payload)
	if status == 0 {
		return false
	}
	s.handleOpenAIAccountUpstreamError(ctx, account, status, headers, payload, canonicalModel)
	return true
}

func (s *OpenAIGatewayService) handleOpenAIWSDialTransientFailure(ctx context.Context, account *Account, canonicalModel string, err error) {
	var dialErr *openAIWSDialError
	if !errors.As(err, &dialErr) || dialErr == nil || !shouldCooldownOpenAITransientUpstreamError(dialErr.StatusCode, dialErr.ResponseBody) {
		return
	}
	s.handleOpenAIAccountUpstreamError(ctx, account, dialErr.StatusCode, dialErr.ResponseHeaders, dialErr.ResponseBody, canonicalModel)
}

func (s *OpenAIGatewayService) ResolveAccountIDByPreviousResponseIDForScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
) int64 {
	accountID, _, _, _ := s.resolveAccountByPreviousResponseIDForCapability(ctx, groupID, previousResponseID, requestedModel, excludedIDs, requiredCapability, requireCompact)
	return accountID
}

func (s *OpenAIGatewayService) resolveAccountByPreviousResponseIDForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
) (int64, *Account, string, OpenAIWSStateStore) {
	if s == nil {
		return 0, nil, "", nil
	}
	responseID := strings.TrimSpace(previousResponseID)
	if responseID == "" {
		return 0, nil, "", nil
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return 0, nil, "", nil
	}

	accountID, err := store.GetResponseAccount(ctx, derefGroupID(groupID), responseID)
	if err != nil || accountID <= 0 {
		return 0, nil, "", nil
	}
	if excludedIDs != nil {
		if _, excluded := excludedIDs[accountID]; excluded {
			return 0, nil, "", nil
		}
	}

	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	// OAuth/SetupToken continuation state lives on the WSv2 session and cannot
	// survive an HTTP fallback. Official API-key Responses HTTP requests are
	// different: previous_response_id is supported by the provider and scoped to
	// the selected key/project, so the response-id binding must retain that key.
	if !account.IsOpenAIApiKey() && s.getOpenAIWSProtocolResolver().Resolve(account).Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
		return 0, nil, "", nil
	}
	if shouldClearStickySession(account, requestedModel) || !account.IsOpenAI() || !account.IsSchedulable() {
		_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) {
		_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	if requestedModel != "" && !account.IsModelSupported(requestedModel) {
		return 0, nil, "", nil
	}
	if !account.SupportsOpenAIEndpointCapability(requiredCapability) {
		return 0, nil, "", nil
	}
	// Quota auto-pause must also gate the previous_response_id sticky path; otherwise an
	// account over its 5h/7d threshold keeps serving the same response chain even though
	// normal scheduling skips it. Pause is transient, so fall through to normal scheduling
	// without deleting the binding (the window may reset before the next turn).
	if paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
		return 0, nil, "", nil
	}
	// 分组利润控制：与 quota auto-pause 同语义——利润不合格是暂时
	// 状态（上游倍率/高峰随时间变化），只跳过本次复用、落回普通调度，不删除
	// 绑定（倍率恢复后可继续按 previous_response_id 粘连）。
	if vetoed, _ := openAIProfitControlVetoReason(ctx, account); vetoed {
		return 0, nil, "", nil
	}
	if s.schedulerSnapshot != nil && s.accountRepo != nil {
		latest, latestErr := s.accountRepo.GetByID(ctx, account.ID)
		if latestErr != nil || latest == nil {
			_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if shouldClearStickySession(latest, requestedModel) || !latest.IsOpenAI() || !latest.IsSchedulable() {
			_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if !s.openAIAccountMatchesSchedulingGroup(latest, groupID) {
			return 0, nil, "", nil
		}
		if s.openAIGroupRequiresPrivacySet(ctx, groupID) && !latest.IsPrivacySet() {
			return 0, nil, "", nil
		}
		if !parentHealthyForShadow(latest, s.parentAccountLookup(ctx)) {
			_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		if requestedModel != "" && !latest.IsModelSupported(requestedModel) {
			return 0, nil, "", nil
		}
		if !latest.SupportsOpenAIEndpointCapability(requiredCapability) {
			return 0, nil, "", nil
		}
		if paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, latest); paused {
			return 0, nil, "", nil
		}
		// 利润门对最新账号状态复检一次，语义同上：跳过复用、不删绑定。
		if vetoed, _ := openAIProfitControlVetoReason(ctx, latest); vetoed {
			return 0, nil, "", nil
		}
		if s.isOpenAIAccountRequestRuntimeBlocked(latest, requestedModel) {
			_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
			return 0, nil, "", nil
		}
		account = latest
	}
	if requireCompact && openAICompactSupportTier(account) == 0 {
		_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
		return 0, nil, "", nil
	}
	return accountID, account, responseID, store
}

func openAIWSSemantic429Headers(account *Account, model string, headers http.Header) http.Header {
	if isCodexSparkModel(model) && isOpenAIOAuthAccount(account) {
		return headers
	}
	return nil
}

func (s *OpenAIGatewayService) newOpenAIWSRateLimitFailoverError(account *Account, headers http.Header, responseBody []byte, message string) *UpstreamFailoverError {
	return s.newOpenAIAccountFailoverError(
		account,
		http.StatusTooManyRequests,
		headers,
		responseBody,
		strings.TrimSpace(message),
		false,
		false,
	)
}

