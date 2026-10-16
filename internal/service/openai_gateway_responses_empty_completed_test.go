package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesEmptyCompletedFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_empty\",\"status\":\"in_progress\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty\",\"status\":\"completed\"}}\n\n",
		)),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_passthrough": true}
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)

	_, err := svc.Forward(context.Background(), c, account, body)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, recorder.Body.String(), "空成功流不能先写给客户端，否则无法安全切换账号")
}

func TestOpenAIResponsesCompletedWithOutputOrUsageSucceeds(t *testing.T) {
	tests := []struct {
		name       string
		streamBody string
		wantText   string
		wantInput  int
	}{
		{
			name: "visible output",
			streamBody: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ok\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
			wantText:  "hello",
			wantInput: 10,
		},
		{
			name: "usage only",
			streamBody: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_usage\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_usage\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n",
			wantInput: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(test.streamBody)),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
			account := newOpenAIImageGenerationControlTestAccount()
			account.Extra = map[string]any{"openai_passthrough": true}
			body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":"continue"}`)

			result, err := svc.Forward(context.Background(), c, account, body)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.Usage)
			require.Equal(t, test.wantInput, result.Usage.InputTokens)
			if test.wantText != "" {
				require.Contains(t, recorder.Body.String(), test.wantText)
			}
		})
	}
}
