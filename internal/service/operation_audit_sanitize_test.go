package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizeAuditJSONNeverKeepsValuesOrCredentialLikeFieldNames(t *testing.T) {
	secretField := "sk-" + strings.Repeat("A", 40)
	raw, err := json.Marshal(map[string]any{
		"password":  "plain-text-password",
		"api_key":   "sk-live-secret-value",
		"status":    "active",
		secretField: "anything",
	})
	require.NoError(t, err)

	summary := SummarizeAuditJSON(raw)
	encoded, err := json.Marshal(summary)
	require.NoError(t, err)
	text := string(encoded)
	require.NotContains(t, text, "plain-text-password")
	require.NotContains(t, text, "sk-live-secret-value")
	require.NotContains(t, text, secretField)
	require.Contains(t, text, "password")
	require.Contains(t, text, "api_key")
	require.Contains(t, text, "field_sha256_")
	require.Contains(t, text, "configured")
}

func TestSanitizeOperationAuditSummaryDropsArbitraryPayloadsAndStates(t *testing.T) {
	summary := SanitizeOperationAuditSummary(map[string]any{
		"fields":  []any{"status", "bad field name", strings.Repeat("x", 100)},
		"states":  map[string]any{"status": "changed", "token": "raw-secret-value"},
		"mode":    "15m",
		"binding": "short-secret",
		"payload": map[string]any{"authorization": "Bearer secret"},
		"body":    "full request body should never survive",
	})
	encoded, err := json.Marshal(summary)
	require.NoError(t, err)
	text := string(encoded)
	require.Contains(t, text, "status")
	require.Contains(t, text, "field_sha256_")
	require.Contains(t, text, "15m")
	require.NotContains(t, text, "raw-secret-value")
	require.NotContains(t, text, "short-secret")
	require.NotContains(t, text, "Bearer secret")
	require.NotContains(t, text, "full request body")
	require.NotContains(t, text, "payload")
}
