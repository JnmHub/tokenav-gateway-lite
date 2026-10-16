package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRewriteResponseModelSSEPayloadUsesCachedPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	beginClientResponseModelPolicy(c, context.Background(), &Account{}, "client-model")

	payload := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"mapped-model\"}}\n\n")
	rewritten := rewriteResponseModelSSEPayload(c, nil, "client-model", payload)

	require.Contains(t, string(rewritten), `"model":"client-model"`)
	require.NotContains(t, string(rewritten), `"model":"mapped-model"`)
}

func TestRewriteResponseModelSSEPayloadPreservesEventsWithoutModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// 没有分组策略时默认跟随请求，验证 helper 不会改写无 model 的事件。
	payload := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"ok\"}}\n\n")
	require.Equal(t, string(payload), string(rewriteResponseModelSSEPayload(c, nil, "client-model", payload)))
}
