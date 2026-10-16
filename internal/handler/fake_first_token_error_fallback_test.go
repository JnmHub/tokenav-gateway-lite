package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// 伪造首字注释行提交 200+SSE 头之后上游失败，错误必须以协议合规的终止事件
// 写给客户端（response.failed / event: error），不允许在注释行后静默结束——
// 否则客户端表现为「很快完成但没有任何回复」（Cherry Studio 空回复问题）。
func TestEnsureForwardErrorResponseAfterFakeFirstTokenWritesSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	// 模拟伪造首字已抢先提交：200 + text/event-stream + 注释行（含记账）。
	if committed, _ := service.CommitFakeFirstTokenToClient(c, time.Now()); !committed {
		t.Fatalf("伪造首字应提交成功")
	}

	wrote := h.ensureForwardErrorResponse(c, false)
	if !wrote {
		t.Fatalf("伪造首字提交后必须补写终止错误，不能静默结束")
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.failed") && !strings.Contains(body, "event: error") {
		t.Fatalf("响应体必须包含终止错误事件, got: %q", body)
	}
	if !strings.Contains(body, "fake-first-token") {
		t.Fatalf("响应体应保留先行的注释行, got: %q", body)
	}
}
