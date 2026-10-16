package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// armFakeFirstTokenBeforeUpstream 在真正请求上游之前武装伪造首字定时器
//（伪造流式首字总开关）。
//
// 为什么必须放在 handler 层：上游经常要十几秒才回响应头，服务层那些
// handleStreamingResponse/streamXxx 函数是在拿到上游响应之后才被调用的，
// 到那时再排程伪造首字已经晚于真实首字节，用户看不到效果。
//
// 定时器随机延迟到点后，若客户端仍未收到任何真实字节，就提交
// HTTP 200 + text/event-stream + 一行 SSE 注释行（: fake-first-token）。
// 注意副作用：状态码从此固化，后续失败只能以流内事件表达
//（response.failed / event: error）；注释行属于非语义字节，会被计入 keepalive
// 字节，因此不阻止安全换号。
//
// 返回的 stop 必须在 handler 退出前调用：gin 会回收 ResponseWriter，
// 晚到的写入会污染后续响应。
func armFakeFirstTokenBeforeUpstream(c *gin.Context, cfg service.FakeFirstTokenSettings, startTime time.Time) func() {
	if c == nil || !cfg.Enabled {
		return func() {}
	}
	return service.ArmFakeFirstToken(c, cfg, startTime)
}
