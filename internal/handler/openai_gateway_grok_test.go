package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAICompatibleRequestPlatformUsesGrokGroup(t *testing.T) {
	apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformGrok}}
	require.Equal(t, service.PlatformGrok, openAICompatibleRequestPlatform(apiKey))
	require.True(t, allowOpenAICompatibleMessagesDispatch(apiKey))
}

func TestOpenAICompatibleRequestPlatformDefaultsToOpenAI(t *testing.T) {
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(nil))
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(&service.APIKey{}))
}
