package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIContextWindowErrorDoesNotFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}`)

	require.True(t, isOpenAIContextWindowError("", body))
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusBadRequest, "", body))
	require.False(t, openAIStreamFailedEventShouldFailover(body, ""))
}

func TestOpenAITransientProcessingStillFailovers(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"message":"An error occurred while processing your request"}}`)

	require.False(t, isOpenAIContextWindowError("", body))
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusBadRequest, "", body))
	require.True(t, openAIStreamFailedEventShouldFailover(body, ""))
}
