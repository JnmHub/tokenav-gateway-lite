package admin

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// TestAI accepts only built-in synthetic cases. An administrator testing adapter
// health does not thereby authorize sending a real customer's private prompt.
func (h *ContentModerationHandler) TestAI(c *gin.Context) {
	var req service.TestContentModerationAIInput
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		response.BadRequest(c, "仅接受 case_id 合成案例标识与可选的已保存配置指纹 config_fingerprint")
		return
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		response.BadRequest(c, "检测请求只能包含一个 JSON 对象")
		return
	}
	result, err := h.service.TestAI(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, "无效的合成案例或配置不可读取")
		return
	}
	// Successful transport does not imply allow: the response always exposes the
	// actual error/not-configured/coverage state for the Win12 operator.
	response.Success(c, result)
}

func (h *ContentModerationHandler) GetAIMetrics(c *gin.Context) {
	metrics, err := h.service.GetAIMetrics(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, metrics)
}
