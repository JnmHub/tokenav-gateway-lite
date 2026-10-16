package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountKeywordPolicyModeResolvesAgainstGroup(t *testing.T) {
	tests := []struct {
		name        string
		extra       map[string]any
		groupAllows bool
		wantMode    string
		wantAllows  bool
	}{
		{name: "missing follows strict group", wantMode: KeywordPolicyModeInherit},
		{name: "missing follows permissive group", groupAllows: true, wantMode: KeywordPolicyModeInherit, wantAllows: true},
		{name: "protect overrides permissive group", extra: map[string]any{KeywordPolicyModeExtraKey: KeywordPolicyModeProtect}, groupAllows: true, wantMode: KeywordPolicyModeProtect},
		{name: "allow overrides strict group", extra: map[string]any{KeywordPolicyModeExtraKey: KeywordPolicyModeAllow}, wantMode: KeywordPolicyModeAllow, wantAllows: true},
		{name: "invalid persisted value fails safe", extra: map[string]any{KeywordPolicyModeExtraKey: "unknown"}, groupAllows: true, wantMode: KeywordPolicyModeProtect},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{Extra: test.extra}
			require.Equal(t, test.wantMode, account.KeywordPolicyMode())
			require.Equal(t, test.wantAllows, account.AcceptsBlockedKeywords(test.groupAllows))
		})
	}
}

func TestKeywordProtectedAccountContextMergesWithoutMutation(t *testing.T) {
	policy := map[int64]struct{}{11: {}}
	ctx := ContextWithKeywordProtectedAccountIDs(context.Background(), policy)
	policy[12] = struct{}{}
	retryExcluded := map[int64]struct{}{21: {}}
	merged := MergeKeywordProtectedAccountIDs(ctx, retryExcluded)

	require.Equal(t, map[int64]struct{}{11: {}, 21: {}}, merged)
	merged[22] = struct{}{}
	require.Equal(t, map[int64]struct{}{11: {}}, KeywordProtectedAccountIDsFromContext(ctx))
	require.Equal(t, map[int64]struct{}{21: {}}, retryExcluded)
}
