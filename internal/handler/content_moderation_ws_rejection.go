package handler

import (
	"context"
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// This notification runs only after transport validation rejects a strict
// create. Do not reuse runContentModeration: its routing/history/refund work
// belongs to ordinary admission and could otherwise touch the previous turn.
func (h *OpenAIGatewayHandler) notifyResponsesWSStrictRejection(ctx context.Context, c *gin.Context, conn *coderws.Conn, apiKey *service.APIKey, subject middleware2.AuthSubject, originalModel string, payload []byte) {
	if h == nil || h.contentModerationService == nil || c == nil || c.Request == nil {
		return
	}
	model := strings.TrimSpace(originalModel)
	input := buildContentModerationInput(c, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, model, payload)
	// The adapter supplies its existing frame/session model resolution. The request
	// context may hold the previous public model; never rewrite that billing state.
	input.Model = model
	input.ResponsesWebSocket = true
	decision := h.contentModerationService.RecordAIWSStrictRejection(c.Request.Context(), input, func() (string, uint64) {
		return nextContentModerationAdmissionTurn(c)
	})
	if decision != nil {
		// Use the existing two-second write budget and client lifecycle; the
		// adapter still returns the original strict error if this write fails.
		writeContentModerationWSError(ctx, conn, decision)
	}
}
