package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const openAIInvalidFunctionParametersBody = `{"error":{"message":"Invalid schema for function 'automation_update'","type":"invalid_request_error","param":"input[8].tools[1].parameters","code":"invalid_function_parameters"}}`

func newOpenAIUpstreamErrorTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, recorder
}

func newOpenAIUpstreamErrorResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHandleErrorResponseDeterministic400PreservesClientDetails(t *testing.T) {
	c, recorder := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "test"}

	_, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAIUpstreamErrorResponse(http.StatusBadRequest, openAIInvalidFunctionParametersBody),
		c,
		account,
		nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
	require.Equal(t, "invalid_function_parameters", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Equal(t, "input[8].tools[1].parameters", gjson.Get(recorder.Body.String(), "error.param").String())
	require.Contains(t, gjson.Get(recorder.Body.String(), "error.message").String(), "automation_update")
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "确定性请求错误换账号仍会失败，不能触发故障切换")
}

func TestHandleErrorResponseDeterministic400WithoutMetadataOrJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "message only", body: `{"error":{"message":"Invalid input"}}`},
		{name: "non json", body: `<html><body>400 Bad Request</body></html>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, recorder := newOpenAIUpstreamErrorTestContext(t)
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "test"}

			_, err := svc.handleErrorResponse(context.Background(), newOpenAIUpstreamErrorResponse(http.StatusBadRequest, test.body), c, account, nil)

			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
			require.NotEmpty(t, gjson.Get(recorder.Body.String(), "error.message").String())
			require.False(t, gjson.Get(recorder.Body.String(), "error.code").Exists())
			require.False(t, gjson.Get(recorder.Body.String(), "error.param").Exists())
		})
	}
}
