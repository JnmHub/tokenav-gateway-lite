package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// 对接网关（new-api / sub2api 原版）的「首字」统计只认 data: 行：
// chat completions 入站的伪造首字必须是真实 role 空首帧，而不是注释行。
func TestFakeFirstTokenChatCompletionsWritesRoleFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request = c.Request.WithContext(service.WithClientRequestedResponseModel(c.Request.Context(), "gpt-5.6-terra"))

	committed, ms := service.CommitFakeFirstTokenToClient(c, time.Now())
	if !committed {
		t.Fatalf("chat 入站伪造首字应提交成功")
	}
	if ms < 0 {
		t.Fatalf("提交时刻异常: %d", ms)
	}
	body := recorder.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("chat 入站必须写 data: 行, got: %q", body)
	}
	if strings.Contains(body, ": fake-first-token") {
		t.Fatalf("chat 入站不应再写注释行, got: %q", body)
	}
	payload := strings.TrimPrefix(strings.TrimRight(strings.SplitN(body, "\n", 2)[0], "\r"), "data: ")
	var chunk struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		t.Fatalf("role 帧必须是合法 JSON chunk: %v, got: %q", err, payload)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Delta.Role != "assistant" || chunk.Choices[0].Delta.Content != "" {
		t.Fatalf("必须是 role 空首帧, got: %q", payload)
	}
	if chunk.Object != "chat.completion.chunk" || chunk.Model != "gpt-5.6-terra" || !strings.HasPrefix(chunk.ID, "chatcmpl-") {
		t.Fatalf("chunk 形态不合规: %+v", chunk)
	}
}

// responses 入站保持注释行：responses 协议事件顺序严格，伪造协议帧风险高。
func TestFakeFirstTokenResponsesStillUsesComment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	if committed, _ := service.CommitFakeFirstTokenToClient(c, time.Now()); !committed {
		t.Fatalf("responses 入站伪造首字应提交成功")
	}
	if !strings.Contains(recorder.Body.String(), ": fake-first-token") {
		t.Fatalf("responses 入站应保持注释行, got: %q", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "chat.completion.chunk") {
		t.Fatalf("responses 入站不应出现 chat chunk, got: %q", recorder.Body.String())
	}
}
