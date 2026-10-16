package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamResponseModelObserverTerminalWins(t *testing.T) {
	observer := &upstreamResponseModelObserver{}
	observer.ObserveOpenAI([]byte(`{"response":{"model":"gpt-early"}}`), "response.created")
	observer.ObserveOpenAI([]byte(`{"response":{"model":"gpt-final"}}`), "response.completed")
	require.Equal(t, "gpt-final", observer.Model())
	require.True(t, observer.Conflict())
}

func TestUpstreamResponseModelObserverProviderShapesAndBounds(t *testing.T) {
	observer := &upstreamResponseModelObserver{}
	observer.ObserveAnthropic([]byte(`{"type":"message_start","message":{"model":"claude-real"}}`))
	require.Equal(t, "claude-real", observer.Model())
	observer = &upstreamResponseModelObserver{}
	observer.ObserveGemini([]byte(`{"modelVersion":"gemini-real"}`))
	require.Equal(t, "gemini-real", observer.Model())
	observer = &upstreamResponseModelObserver{}
	observer.ObserveGemini([]byte(`{"response":{"response":{"modelVersion":"gemini-nested"}}}`))
	require.Equal(t, "gemini-nested", observer.Model())
	observer.Observe(strings.Repeat("模", 250), true)
	require.Len(t, []rune(observer.Model()), upstreamResponseModelMaxLength)
}

func TestUpstreamResponseModelObserverIgnoresMalformedCandidate(t *testing.T) {
	observer := &upstreamResponseModelObserver{}
	observer.ObserveOpenAI([]byte(`{"model":"spoof"`), "response.completed")
	require.Empty(t, observer.Model())
}

func TestUpstreamModelMismatch(t *testing.T) {
	require.Nil(t, upstreamModelMismatch("gpt-a", ""))
	require.False(t, *upstreamModelMismatch("GPT-A", "gpt-a"))
	require.False(t, *upstreamModelMismatch("grok-4.5-latest", "grok-4.5-build"))
	require.False(t, *upstreamModelMismatch("grok-4.6", "grok-4.6-latest"))
	require.True(t, *upstreamModelMismatch("gpt-a", "gpt-b"))
}
