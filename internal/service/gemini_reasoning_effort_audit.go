package service

import (
	"io"
	"net/http"
)

// extractGeminiReasoningEffortAudit reads only an explicit Gemini
// thinkingLevel from the exact payload sent upstream. Code Assist and
// Antigravity wrap the native request under request; AI Studio and Vertex send
// it directly. A numeric thinkingBudget deliberately does not imply a level.
func extractGeminiReasoningEffortAudit(body []byte) *string {
	return extractReasoningEffortAuditFromBody(
		body,
		"generationConfig.thinkingConfig.thinkingLevel",
		"request.generationConfig.thinkingConfig.thinkingLevel",
	)
}

func extractGeminiReasoningEffortAuditFromRequest(req *http.Request) *string {
	if req == nil || req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil
	}
	defer func() { _ = body.Close() }()
	payload, err := io.ReadAll(body)
	if err != nil {
		return nil
	}
	return extractGeminiReasoningEffortAudit(payload)
}
