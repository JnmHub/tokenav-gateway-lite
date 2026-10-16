package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

type GatewayBindingHandler struct{}

func NewGatewayBindingHandler() *GatewayBindingHandler { return &GatewayBindingHandler{} }
func bindingManager(c *gin.Context) *gatewaylite.BindingManager {
	c.Header("Cache-Control", "no-store")
	m := gatewaylite.DefaultBindingManager()
	if m == nil {
		response.Error(c, 503, "binding_state_unavailable")
	}
	return m
}
func (*GatewayBindingHandler) Status(c *gin.Context) {
	if m := bindingManager(c); m != nil {
		response.Success(c, m.Status())
	}
}
func bindingBody(c *gin.Context) (gatewaylite.BindingInput, bool) {
	var in gatewaylite.BindingInput
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "binding_request_invalid")
		return in, false
	}
	return in, true
}
func (*GatewayBindingHandler) Probe(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	in, ok := bindingBody(c)
	if !ok {
		return
	}
	if err := m.Probe(c.Request.Context(), in); err != nil {
		bindingFailure(c, err)
		return
	}
	response.Success(c, gin.H{"verified": true})
}
func (*GatewayBindingHandler) Verify(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	in, ok := bindingBody(c)
	if !ok {
		return
	}
	if err := m.Verify(c.Request.Context(), in); err != nil {
		bindingFailure(c, err)
		return
	}
	response.Success(c, gin.H{"verified": true})
}
func (*GatewayBindingHandler) Register(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	in, ok := bindingBody(c)
	if !ok {
		return
	}
	status, err := m.Register(c.Request.Context(), in)
	if err != nil {
		bindingFailure(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"code": 0, "message": "节点已登记，正在重启并验证实际连接。", "data": status})
}
func (*GatewayBindingHandler) DisconnectCheck(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	result, err := m.DisconnectCheck(ctx)
	if err != nil {
		c.JSON(503, gin.H{"code": 503, "message": "结算检查暂不可用，请重试。", "data": result})
		return
	}
	response.Success(c, result)
}
func (*GatewayBindingHandler) Disconnect(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if c.ShouldBindJSON(&in) != nil || !in.Confirm {
		response.BadRequest(c, "binding_confirmation_required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	result, err := m.Disconnect(ctx)
	if err != nil {
		status := 409
		if !errors.Is(err, gatewaylite.ErrBindingConflict) && err.Error() != "binding_undrained" {
			status = 503
		}
		message := "旧主站未完成结算或连接检查，已停止新增付费请求；可重试或取消断开。"
		if m.Status().DisconnectPending {
			message = "断开正在核验远端提交结果，请重试断开完成确认；此阶段不能取消恢复原连接。"
		}
		c.JSON(status, gin.H{"code": status, "message": message, "data": result})
		return
	}
	response.Success(c, m.Status())
}
func (*GatewayBindingHandler) Cancel(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	if err := m.Cancel(c.Request.Context()); err != nil {
		bindingFailure(c, err)
		return
	}
	response.Success(c, m.Status())
}
func (*GatewayBindingHandler) Retry(c *gin.Context) {
	m := bindingManager(c)
	if m == nil {
		return
	}
	if m.Status().Phase != "activating" || !m.RetryActivation() {
		response.Error(c, 409, "binding_restart_unavailable")
		return
	}
	c.JSON(202, gin.H{"code": 0, "data": m.Status()})
}
func bindingFailure(c *gin.Context, err error) {
	status := 400
	switch err.Error() {
	case "unauthorized":
		status = 401
	case "binding_node_conflict", "binding_state_conflict", "binding_registration_pending_retry_same_destination":
		status = 409
	case "binding_control_plane_unreachable", "binding_restart_unavailable", "binding_response_invalid", "binding_control_plane_rejected":
		status = 503
	}
	// A wrong enrolment secret is not an expired gateway administrator session.
	// Keep 401 in the remote error code but don't trigger frontend auto-logout.
	if status == 401 {
		status = 400
	}
	response.Error(c, status, err.Error())
}
