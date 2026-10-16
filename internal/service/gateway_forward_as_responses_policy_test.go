package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesAnthropicFallbackUsesClientModelPolicyAfterMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := WithClientRequestedResponseModel(request.Context(), "public-model")
	c.Request = request.WithContext(ctx)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, &Group{
		ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest},
	}))
	beginClientResponseModelPolicy(c, c.Request.Context(), &Account{}, "mapped-model")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"x-request-id": []string{"rid-policy"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_policy","type":"message","role":"assistant","content":[],"model":"actual-upstream-model","stop_reason":"","usage":{"input_tokens":1}}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			``,
		}, "\n"))),
	}

	result, err := (&GatewayService{}).handleResponsesBufferedStreamingResponse(
		resp, c, "mapped-model", "mapped-upstream-model", nil, time.Now(), apicompat.ResponsesClientToolMapping{},
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "public-model", gjson.Get(recorder.Body.String(), "model").String())
	require.Equal(t, "actual-upstream-model", result.UpstreamResponseModel)
	require.NotNil(t, result.UpstreamModelMismatch)
	require.True(t, *result.UpstreamModelMismatch)
}
