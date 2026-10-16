package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRewriteOpenAIWSPassthroughResponseModelHonorsPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newContext := func(mode, custom string) *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		request := httptest.NewRequest("GET", "/v1/responses", nil)
		ctx := context.WithValue(request.Context(), ctxkey.Group, &Group{ModelsListConfig: GroupModelsListConfig{
			ResponseModelMode: mode, ResponseModelCustom: custom,
		}})
		c.Request = request.WithContext(ctx)
		// 连接首帧缓存为 initial-model；第二轮必须按自身请求模型改写。
		beginClientResponseModelPolicy(c, ctx, &Account{}, "initial-model")
		return c
	}
	payload := []byte(`{"type":"response.completed","response":{"model":"upstream-model"}}`)

	requestContext := newContext(ResponseModelModeRequest, "")
	requestResult := rewriteOpenAIWSPassthroughResponseModel(requestContext.Request.Context(), &Account{}, "turn-two-model", payload)
	require.JSONEq(t, `{"type":"response.completed","response":{"model":"turn-two-model"}}`, string(requestResult))

	customContext := newContext(ResponseModelModeCustom, "public-model")
	customResult := rewriteOpenAIWSPassthroughResponseModel(customContext.Request.Context(), &Account{}, "turn-two-model", payload)
	require.JSONEq(t, `{"type":"response.completed","response":{"model":"public-model"}}`, string(customResult))

	upstreamContext := newContext(ResponseModelModeUpstream, "")
	upstreamResult := rewriteOpenAIWSPassthroughResponseModel(upstreamContext.Request.Context(), &Account{}, "turn-two-model", payload)
	require.JSONEq(t, string(payload), string(upstreamResult))

	// session.* and future event names may carry model declarations too; the
	// response policy must not depend on an event-type allowlist.
	sessionPayload := []byte(`{"type":"session.created","session":{"model":"upstream-model"}}`)
	sessionResult := rewriteOpenAIWSPassthroughResponseModel(requestContext.Request.Context(), &Account{}, "turn-two-model", sessionPayload)
	require.JSONEq(t, `{"type":"session.created","session":{"model":"turn-two-model"}}`, string(sessionResult))
	futurePayload := []byte(`{"type":"response.future","model":"upstream-model"}`)
	futureResult := rewriteOpenAIWSPassthroughResponseModel(requestContext.Request.Context(), &Account{}, "turn-two-model", futurePayload)
	require.JSONEq(t, `{"type":"response.future","model":"turn-two-model"}`, string(futureResult))
}

func TestValidateOpenAIWSPassthroughClientFrame(t *testing.T) {
	tests := []struct {
		name        string
		messageType coderws.MessageType
		payload     string
		wantStatus  coderws.StatusCode
		wantReason  string
	}{
		{name: "response create", messageType: coderws.MessageText, payload: `{"type":"response.create"}`},
		{name: "response cancel", messageType: coderws.MessageText, payload: `{"type":"response.cancel"}`},
		{name: "session update", messageType: coderws.MessageText, payload: `{"type":"session.update","session":{"model":"gpt-5.6-sol"}}`},
		{name: "conversation item", messageType: coderws.MessageText, payload: `{"type":"conversation.item.create","item":{}}`},
		{name: "binary JSON", messageType: coderws.MessageBinary, payload: `{"type":"response.create"}`},
		{name: "binary media", messageType: coderws.MessageBinary, payload: "\x00\x01\x02"},
		{name: "malformed", messageType: coderws.MessageText, payload: `{"type":`, wantStatus: coderws.StatusPolicyViolation, wantReason: "invalid websocket request payload"},
		{name: "missing type", messageType: coderws.MessageText, payload: `{"model":"gpt-5.6-sol"}`, wantStatus: coderws.StatusPolicyViolation, wantReason: "invalid websocket request payload"},
		{name: "unknown type", messageType: coderws.MessageText, payload: `{"type":"response.future"}`, wantStatus: coderws.StatusPolicyViolation, wantReason: "unsupported websocket event type: response.future"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpenAIWSPassthroughClientFrame(tt.messageType, []byte(tt.payload))
			if tt.wantStatus == 0 {
				require.NoError(t, err)
				return
			}
			var closeErr *OpenAIWSClientCloseError
			require.True(t, errors.As(err, &closeErr))
			require.Equal(t, tt.wantStatus, closeErr.StatusCode())
			require.Equal(t, tt.wantReason, closeErr.Reason())
		})
	}
}

func TestOpenAIWSFinalUpstreamModelKeepsEqualModel(t *testing.T) {
	require.Equal(t, "gpt-5.6-sol", openAIWSFinalUpstreamModel("gpt-5.6-sol", "gpt-5.6-sol"))
	require.Equal(t, "gpt-5.6-luna", openAIWSFinalUpstreamModel("gpt-5.6-sol", "gpt-5.6-luna"))
	require.Equal(t, "gpt-5.6-sol", openAIWSFinalUpstreamModel("gpt-5.6-sol", ""))
	require.Empty(t, openAIWSFinalUpstreamModel("", ""))
}

func TestValidateOpenAIWSPassthroughUpstreamFrameRejectsProtocolViolations(t *testing.T) {
	completed := make(map[string]struct{})
	require.NoError(t, validateOpenAIWSPassthroughUpstreamFrame(
		coderws.MessageText,
		[]byte(`{"type":"response.output_text.delta","response_id":"resp_1","delta":"ok"}`),
		completed,
	))
	require.NoError(t, validateOpenAIWSPassthroughUpstreamFrame(
		coderws.MessageText,
		[]byte(`{"type":"response.completed","response":{"id":"resp_1"}}`),
		completed,
	))

	err := validateOpenAIWSPassthroughUpstreamFrame(
		coderws.MessageText,
		[]byte(`{"type":"response.done","response":{"id":"resp_1"}}`),
		completed,
	)
	require.ErrorContains(t, err, "after terminal response resp_1")

	err = validateOpenAIWSPassthroughUpstreamFrame(
		coderws.MessageText,
		[]byte(`{"type":"response.output_text.delta","response_id":"resp_1","delta":"late"}`),
		completed,
	)
	require.ErrorContains(t, err, "after terminal response resp_1")

	require.NoError(t, validateOpenAIWSPassthroughUpstreamFrame(coderws.MessageBinary, []byte(`{"type":"response.completed","response":{"id":"resp_binary"}}`), make(map[string]struct{})))
	require.NoError(t, validateOpenAIWSPassthroughUpstreamFrame(coderws.MessageBinary, []byte{0x00, 0x01, 0x02}, make(map[string]struct{})))
	err = validateOpenAIWSPassthroughUpstreamFrame(coderws.MessageText, []byte(`{"type":`), make(map[string]struct{}))
	require.ErrorContains(t, err, "invalid Responses event")
	err = validateOpenAIWSPassthroughUpstreamFrame(coderws.MessageText, []byte(`{"response":{"id":"resp_2"}}`), make(map[string]struct{}))
	require.ErrorContains(t, err, "event type is required")
}
