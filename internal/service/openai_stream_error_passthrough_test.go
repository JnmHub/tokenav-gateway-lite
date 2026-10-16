package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestApplyOpenAIStreamFailedErrorPassthroughRuleNormalizesNestedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	customStatus := http.StatusTeapot
	customMessage := "上游参数暂不可用"
	ruleService := &ErrorPassthroughService{}
	ruleService.setLocalCache([]*model.ErrorPassthroughRule{{
		ID:              1,
		Name:            "stream-failed",
		Enabled:         true,
		Priority:        1,
		Platforms:       []string{PlatformOpenAI},
		ErrorCodes:      []int{http.StatusBadRequest},
		Keywords:        []string{"invalid schema"},
		MatchMode:       model.MatchModeAll,
		ResponseCode:    &customStatus,
		CustomMessage:   &customMessage,
		PassthroughCode: false,
		PassthroughBody: false,
	}})
	BindErrorPassthroughService(c, ruleService)

	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"bad_arguments","message":"Invalid schema for tools"}}}`)
	status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, PlatformOpenAI, payload, "")

	require.True(t, matched)
	require.Equal(t, http.StatusTeapot, status)
	require.Equal(t, "upstream_error", errType)
	require.Equal(t, customMessage, errMsg)
}

func TestApplyOpenAIStreamFailedErrorPassthroughRuleKeepsTopLevelError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ruleService := &ErrorPassthroughService{}
	ruleService.setLocalCache([]*model.ErrorPassthroughRule{{
		ID:              2,
		Name:            "stream-error",
		Enabled:         true,
		Priority:        1,
		Platforms:       []string{PlatformGrok},
		ErrorCodes:      []int{http.StatusBadRequest},
		Keywords:        []string{"bad input"},
		MatchMode:       model.MatchModeAll,
		PassthroughCode: true,
		PassthroughBody: true,
	}})
	BindErrorPassthroughService(c, ruleService)

	payload := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}`)
	status, _, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, PlatformGrok, payload, "")

	require.True(t, matched)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "bad input", errMsg)
}

func TestOpenAIStreamingHandlersApplyErrorPassthroughBeforeOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		run  func(*OpenAIGatewayService, *gin.Context, *http.Response, *Account) error
	}{
		{
			name: "native",
			run: func(service *OpenAIGatewayService, c *gin.Context, response *http.Response, account *Account) error {
				_, err := service.handleStreamingResponse(context.Background(), response, c, account, time.Now(), "gpt-test", "gpt-test")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(service *OpenAIGatewayService, c *gin.Context, response *http.Response, account *Account) error {
				_, err := service.handleStreamingResponsePassthrough(context.Background(), response, c, account, time.Now(), "gpt-test", "gpt-test")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			customStatus := http.StatusTeapot
			customMessage := "上游参数暂不可用"
			ruleService := &ErrorPassthroughService{}
			ruleService.setLocalCache([]*model.ErrorPassthroughRule{{
				ID:              3,
				Name:            "stream-handler",
				Enabled:         true,
				Priority:        1,
				Platforms:       []string{PlatformOpenAI},
				ErrorCodes:      []int{http.StatusBadRequest},
				Keywords:        []string{"invalid schema"},
				MatchMode:       model.MatchModeAll,
				ResponseCode:    &customStatus,
				CustomMessage:   &customMessage,
				PassthroughCode: false,
				PassthroughBody: false,
			}})
			BindErrorPassthroughService(c, ruleService)
			service := &OpenAIGatewayService{cfg: &config.Config{}}
			stream := strings.Join([]string{
				"event: response.failed",
				`data: {"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"bad_arguments","message":"Invalid schema for tools"}}}`,
				"",
			}, "\n")
			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(stream)),
			}

			err := test.run(service, c, response, &Account{ID: 1, Platform: PlatformOpenAI})

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, http.StatusTeapot, recorder.Code)
			require.Contains(t, recorder.Body.String(), customMessage)
			require.NotContains(t, recorder.Body.String(), "response.failed")
		})
	}
}
