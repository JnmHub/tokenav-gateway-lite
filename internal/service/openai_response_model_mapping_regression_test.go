//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// newResponseModelRegressionContext builds the same public-model context that
// the HTTP handler creates before applying a group/account model mapping. The
// direct response handlers below then receive the mapped model, which catches
// regressions where a response writer accidentally uses that mapped value.
func newResponseModelRegressionContext(t *testing.T, requestedModel string) (*gin.Context, *httptest.ResponseRecorder, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	group := &Group{
		ID:       77,
		Platform: PlatformOpenAI,
		Status:   StatusActive,
		Hydrated: true,
		ModelsListConfig: GroupModelsListConfig{
			// request（跟随用户请求） is the default and is the policy that
			// was reported as leaking the mapped upstream model.
			ResponseModelMode: ResponseModelModeRequest,
		},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	ctx = WithClientRequestedResponseModel(ctx, requestedModel)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	beginClientResponseModelPolicy(c, ctx, account, "mapped-upstream-model")
	beginUpstreamResponseModelObservation(c)
	return c, recorder, ctx
}

func TestResponseModelMappingRegression_HTTPJSONKeepsClientModel(t *testing.T) {
	const (
		requestedModel = "gpt-5.6-terra"
		upstreamModel  = "gpt-5.6-luna"
	)

	c, recorder, ctx := newResponseModelRegressionContext(t, requestedModel)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp-regression-json","object":"response","model":"` + upstreamModel + `","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`,
		)),
	}
	defer resp.Body.Close()
	result, err := (&OpenAIGatewayService{}).handleNonStreamingResponse(
		ctx, resp, c, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		upstreamModel, upstreamModel,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, requestedModel, gjson.GetBytes(recorder.Body.Bytes(), "model").String())
	require.Equal(t, upstreamModel, observedUpstreamResponseModel(c))
}

func TestResponseModelMappingRegression_SSEKeepsClientModel(t *testing.T) {
	const (
		requestedModel = "gpt-5.6-sol"
		upstreamModel  = "gpt-5.6-luna"
	)

	c, recorder, ctx := newResponseModelRegressionContext(t, requestedModel)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp-regression-sse","model":"` + upstreamModel + `","status":"in_progress","output":[]}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp-regression-sse","model":"` + upstreamModel + `","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}
	defer resp.Body.Close()

	result, err := (&OpenAIGatewayService{}).handleStreamingResponse(
		ctx, resp, c, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		time.Now(), upstreamModel, upstreamModel,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	body := recorder.Body.String()
	require.Contains(t, body, `"model":"`+requestedModel+`"`)
	require.NotContains(t, body, `"model":"`+upstreamModel+`"`)
	require.Equal(t, upstreamModel, observedUpstreamResponseModel(c))
}

func TestResponseModelMappingRegression_ResponsesChatFallbackHTTPJSONKeepsClientModel(t *testing.T) {
	const (
		requestedModel = "gpt-5.6-terra"
		upstreamModel  = "gpt-5.6-luna"
	)

	c, recorder, _ := newResponseModelRegressionContext(t, requestedModel)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chat-fallback-json","object":"chat.completion","model":"` + upstreamModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		)),
	}
	defer resp.Body.Close()

	result, err := (&OpenAIGatewayService{}).bufferChatCompletionsAsResponses(
		c,
		resp,
		upstreamModel,
		nil,
		false,
		nil,
		upstreamModel,
		upstreamModel,
		nil,
		time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, requestedModel, gjson.GetBytes(recorder.Body.Bytes(), "model").String())
	require.NotContains(t, recorder.Body.String(), `"model":"`+upstreamModel+`"`)
	require.Equal(t, upstreamModel, result.UpstreamResponseModel)
}

func TestResponseModelMappingRegression_ResponsesChatFallbackSSEKeepsClientModel(t *testing.T) {
	const (
		requestedModel = "gpt-5.6-sol"
		upstreamModel  = "gpt-5.6-luna"
	)

	c, recorder, _ := newResponseModelRegressionContext(t, requestedModel)
	streamChunks := []string{
		`data: {"id":"chat-fallback-sse","object":"chat.completion.chunk","model":"` + upstreamModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chat-fallback-sse","object":"chat.completion.chunk","model":"` + upstreamModel + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		"",
		"data: [DONE]",
		"",
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(streamChunks, "\n"))),
	}
	defer resp.Body.Close()

	result, err := (&OpenAIGatewayService{}).streamChatCompletionsAsResponses(
		c,
		resp,
		upstreamModel,
		nil,
		false,
		nil,
		upstreamModel,
		upstreamModel,
		nil,
		time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	body := recorder.Body.String()
	require.Contains(t, body, `"model":"`+requestedModel+`"`)
	require.NotContains(t, body, `"model":"`+upstreamModel+`"`)
	require.Equal(t, upstreamModel, result.UpstreamResponseModel)
}

func TestResponseModelMappingRegression_ResponsesChatFallbackOutputIsValidJSON(t *testing.T) {
	const (
		requestedModel = "gpt-5.6-terra"
		upstreamModel  = "gpt-5.6-luna"
	)

	c, recorder, _ := newResponseModelRegressionContext(t, requestedModel)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chat-fallback-valid-json","model":"` + upstreamModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		)),
	}
	defer resp.Body.Close()

	_, err := (&OpenAIGatewayService{}).bufferChatCompletionsAsResponses(
		c,
		resp,
		upstreamModel,
		map[string]bool{},
		false,
		nil,
		upstreamModel,
		upstreamModel,
		nil,
		time.Now(),
	)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &decoded))
	require.Equal(t, requestedModel, decoded["model"])
}

func TestResponseModelMappingRegression_ContextHelperKeepsFirstPublicModel(t *testing.T) {
	ctx := WithClientRequestedResponseModel(context.Background(), "gpt-5.6-terra")
	ctx = WithClientRequestedResponseModel(ctx, "gpt-5.6-luna")
	requested, ok := clientRequestedResponseModel(ctx)
	require.True(t, ok)
	// A later compatibility layer may see a mapped body, but the explicit
	// public model context remains the first client declaration and cannot be
	// replaced by an upstream value accidentally captured by a retry.
	require.Equal(t, "gpt-5.6-terra", requested)
}
