//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequiresBillableGrokChatUsage(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		models  []string
		want    bool
	}{
		{name: "Grok account", account: &Account{Platform: PlatformGrok}, models: []string{"alias"}, want: true},
		{name: "mapped Grok model", account: &Account{Platform: PlatformOpenAI}, models: []string{"alias", "grok-4.5"}, want: true},
		{name: "qualified Grok model", account: &Account{Platform: PlatformOpenAI}, models: []string{"x-ai/grok-4.5"}, want: true},
		{name: "ordinary model", account: &Account{Platform: PlatformOpenAI}, models: []string{"gpt-5.4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, requiresBillableGrokChatUsage(tt.account, tt.models...))
		})
	}
}

func TestHasBillableGrokChatUsageRequiresAggregateTokens(t *testing.T) {
	require.False(t, hasBillableGrokChatUsage(OpenAIUsage{}))
	require.False(t, hasBillableGrokChatUsage(OpenAIUsage{ImageInputTokens: 4, ImageOutputTokens: 2}))
	require.True(t, hasBillableGrokChatUsage(OpenAIUsage{InputTokens: 1}))
	require.True(t, hasBillableGrokChatUsage(OpenAIUsage{OutputTokens: 1}))
}
