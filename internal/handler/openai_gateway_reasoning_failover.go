package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// openAIPassthroughFailoverState remembers whether this forwarding loop has
// visited a passthrough account. The flag stays sticky across retries because
// provider-specific reasoning can remain in the canonical conversation body.
type openAIPassthroughFailoverState struct {
	passthroughSeen bool
}

func (h *OpenAIGatewayHandler) deriveOpenAIForwardAttemptBody(
	reqLog *zap.Logger,
	canonicalBody []byte,
	account *service.Account,
	state *openAIPassthroughFailoverState,
) []byte {
	if account == nil || state == nil {
		return canonicalBody
	}
	if account.IsOpenAIPassthroughEnabled() {
		state.passthroughSeen = true
		return canonicalBody
	}
	if !state.passthroughSeen {
		return canonicalBody
	}
	sanitized, changed, err := service.SanitizeOpenAICrossModeFailoverReasoning(canonicalBody)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("openai.failover_cross_mode_reasoning_sanitize_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
		return canonicalBody
	}
	if !changed {
		return canonicalBody
	}
	if reqLog != nil {
		reqLog.Info("openai.failover_cross_mode_reasoning_stripped", zap.Int64("account_id", account.ID), zap.Bool("passthrough_seen", true))
	}
	return sanitized
}
