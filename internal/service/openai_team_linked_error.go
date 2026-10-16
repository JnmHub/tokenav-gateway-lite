package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	openAITeamLinkedErrorDedupTTL      = 60 * time.Second
	openAITeamLinkedErrorFanoutTimeout = 30 * time.Second
	openAITeamLinkedErrorBlockReason   = "team_linked_error"
)

// maybeHandleOpenAITeamLinkedError 将已停用 ChatGPT Team 的其他 OAuth 账号同步熔断。
// 这里只识别确定性的 402 deactivated_workspace，普通 429 仍按账号自身限流处理，避免误伤。
func (s *RateLimitService) maybeHandleOpenAITeamLinkedError(ctx context.Context, account *Account, statusCode int, responseBody []byte) {
	if s == nil || s.accountRepo == nil || statusCode != http.StatusPaymentRequired || !isOpenAIOAuthAccount(account) {
		return
	}
	if gjson.GetBytes(responseBody, "detail.code").String() != "deactivated_workspace" {
		return
	}
	teamID := strings.TrimSpace(account.GetChatGPTAccountID())
	if teamID == "" || !s.markOpenAITeamLinkedFired(teamID) {
		return
	}

	// 上游错误的请求上下文可能已经被取消；联动落库必须独立完成。
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAITeamLinkedErrorFanoutTimeout)
	defer cancel()
	accounts, err := s.accountRepo.ListByPlatform(opCtx, PlatformOpenAI)
	if err != nil {
		slog.Warn("openai_team_linked_error_list_failed", "trigger_account_id", account.ID, "error", err)
		return
	}

	targets := make([]*Account, 0)
	for index := range accounts {
		candidate := &accounts[index]
		if candidate.ID == account.ID || !candidate.IsActive() || candidate.IsShadow() || !isOpenAIOAuthAccount(candidate) || strings.TrimSpace(candidate.GetChatGPTAccountID()) != teamID {
			continue
		}
		targets = append(targets, candidate)
	}
	if len(targets) == 0 {
		return
	}

	// 先即时阻断全部关联账号，再依次落库，避免等待数据库时继续被调度。
	for _, target := range targets {
		s.notifyAccountSchedulingBlocked(target, time.Time{}, openAITeamLinkedErrorBlockReason)
	}
	errorMsg := fmt.Sprintf("Workspace deactivated (402): team-linked error triggered by account #%d", account.ID)
	marked := 0
	for _, target := range targets {
		if err := s.accountRepo.SetError(opCtx, target.ID, errorMsg); err != nil {
			slog.Warn("openai_team_linked_error_set_error_failed", "account_id", target.ID, "error", err)
			continue
		}
		marked++
	}
	slog.Warn("openai_team_linked_error_fanout",
		"trigger_account_id", account.ID,
		"chatgpt_account_id", teamID,
		"affected", marked,
		"targets", len(targets),
	)
}

// markOpenAITeamLinkedFired ensures only one fan-out is executed per Team in each 60 second window.
func (s *RateLimitService) markOpenAITeamLinkedFired(teamID string) bool {
	now := time.Now()
	s.openAITeamLinkedMu.Lock()
	defer s.openAITeamLinkedMu.Unlock()
	if expiry, ok := s.openAITeamLinkedRecent[teamID]; ok && expiry.After(now) {
		return false
	}
	if s.openAITeamLinkedRecent == nil {
		s.openAITeamLinkedRecent = make(map[string]time.Time)
	}
	for key, expiry := range s.openAITeamLinkedRecent {
		if !expiry.After(now) {
			delete(s.openAITeamLinkedRecent, key)
		}
	}
	s.openAITeamLinkedRecent[teamID] = now.Add(openAITeamLinkedErrorDedupTTL)
	return true
}
