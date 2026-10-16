package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountIsOpenAILongContextBillingEnabled(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "nil account", account: nil, want: false},
		{name: "missing value defaults off", account: &Account{Platform: PlatformOpenAI}, want: false},
		{name: "explicit false", account: &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAILongContextBillingEnabledKey: false}}, want: false},
		{name: "explicit true", account: &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAILongContextBillingEnabledKey: true}}, want: true},
		{name: "malformed value defaults off", account: &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAILongContextBillingEnabledKey: "true"}}, want: false},
		{name: "non openai account stays off", account: &Account{Platform: PlatformAnthropic, Extra: map[string]any{openAILongContextBillingEnabledKey: true}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.account.IsOpenAILongContextBillingEnabled())
		})
	}
}

func TestNormalizeOpenAILongContextBillingExtra(t *testing.T) {
	t.Run("new openai account defaults off without mutating caller map", func(t *testing.T) {
		input := map[string]any{"keep": "value"}
		normalized, err := normalizeOpenAILongContextBillingExtra(PlatformOpenAI, input)
		require.NoError(t, err)
		require.Equal(t, false, normalized[openAILongContextBillingEnabledKey])
		require.Equal(t, "value", normalized["keep"])
		require.NotContains(t, input, openAILongContextBillingEnabledKey)
	})

	t.Run("explicit true is preserved", func(t *testing.T) {
		normalized, err := normalizeOpenAILongContextBillingExtra(PlatformOpenAI, map[string]any{
			openAILongContextBillingEnabledKey: true,
		})
		require.NoError(t, err)
		require.Equal(t, true, normalized[openAILongContextBillingEnabledKey])
	})

	t.Run("malformed value is rejected", func(t *testing.T) {
		_, err := normalizeOpenAILongContextBillingExtra(PlatformOpenAI, map[string]any{
			openAILongContextBillingEnabledKey: "true",
		})
		require.Error(t, err)
	})

	t.Run("non openai account cannot receive the field", func(t *testing.T) {
		_, err := normalizeOpenAILongContextBillingExtra(PlatformAnthropic, map[string]any{
			openAILongContextBillingEnabledKey: true,
		})
		require.Error(t, err)
	})
}

func TestNormalizeOpenAILongContextBillingUpdateExtra(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			openAILongContextBillingEnabledKey: true,
			"keep":                             "old",
		},
	}

	t.Run("omitted field preserves current value", func(t *testing.T) {
		normalized, err := normalizeOpenAILongContextBillingUpdateExtra(account, map[string]any{"keep": "new"})
		require.NoError(t, err)
		require.Equal(t, true, normalized[openAILongContextBillingEnabledKey])
		require.Equal(t, "new", normalized["keep"])
	})

	t.Run("explicit false overrides current value", func(t *testing.T) {
		normalized, err := normalizeOpenAILongContextBillingUpdateExtra(account, map[string]any{
			openAILongContextBillingEnabledKey: false,
		})
		require.NoError(t, err)
		require.Equal(t, false, normalized[openAILongContextBillingEnabledKey])
	})
}
