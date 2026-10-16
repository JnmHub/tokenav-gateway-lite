package middleware

import (
	"testing"
	"time"
)

func TestGatewayLiteUsageEventCarriesSessionIDAsAuditOnly(t *testing.T) {
	startedAt := time.Unix(100, 0)
	event := gatewayLiteUsageEvent(gatewayLiteFinalizeInput{
		RequestID: "request-idempotency-1",
		SessionID: "session-audit-1",
		UserID:    7,
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(time.Second),
	}, 3)
	if event.RequestID != "request-idempotency-1" || event.SessionID != "session-audit-1" {
		t.Fatalf("unexpected audit identity: request_id=%q session_id=%q", event.RequestID, event.SessionID)
	}
}
