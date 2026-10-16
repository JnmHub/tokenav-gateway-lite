package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAccountConcurrencyForGrokOAuth(t *testing.T) {
	require.Equal(t, 1, normalizeAccountConcurrency(PlatformGrok, AccountTypeOAuth, 0))
	require.Equal(t, 1, normalizeAccountConcurrency(PlatformGrok, AccountTypeOAuth, -5))
	require.Equal(t, 50, normalizeAccountConcurrency(PlatformGrok, AccountTypeOAuth, 50))
	require.Equal(t, 0, normalizeAccountConcurrency(PlatformGrok, AccountTypeAPIKey, 0))
	require.Equal(t, 0, normalizeAccountConcurrency(PlatformOpenAI, AccountTypeOAuth, 0))
}
