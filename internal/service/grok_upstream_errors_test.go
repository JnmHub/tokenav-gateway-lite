package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsGrokContentPolicyRejection(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"new sensitive code", http.StatusForbidden, `{"error":{"code":"new_sensitive","message":"image is sensitive"}}`, true},
		{"content policy code", http.StatusForbidden, `{"response":{"error":{"code":"content_policy_violation"}}}`, true},
		{"explicit moderation rejection", http.StatusForbidden, `{"error":{"message":"request rejected by content moderation"}}`, true},
		{"subscription forbidden", http.StatusForbidden, `{"error":{"message":"subscription required"}}`, false},
		{"account suspension overrides policy words", http.StatusForbidden, `{"error":{"message":"account suspended due to policy violation"}}`, false},
		{"structured suspension overrides reason", http.StatusForbidden, `{"error":{"code":"account_suspended","reason":"content_policy_violation"}}`, false},
		{"ambiguous policy code", http.StatusForbidden, `{"error":{"code":"policy_violation","message":"policy violation"}}`, false},
		{"request scoped policy message", http.StatusForbidden, `{"error":{"code":"policy_violation","message":"request blocked by policy"}}`, true},
		{"wrong status", http.StatusBadRequest, `{"error":{"code":"new_sensitive"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokContentPolicyRejection(tt.status, []byte(tt.body)))
		})
	}
}

func TestGrokRequestScoped403DoesNotDisableAccountAccessErrors(t *testing.T) {
	require.True(t, isGrokRequestScopedPolicyError(http.StatusForbidden, []byte(`{"error":{"code":"content_filter"}}`)))
	require.False(t, isGrokRequestScopedPolicyError(http.StatusForbidden, []byte(`{"error":{"code":"account_suspended","message":"policy violation"}}`)))
}
