package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type OperationAuditHandler struct {
	service *service.OperationAuditService
}

func NewOperationAuditHandler(auditService *service.OperationAuditService) *OperationAuditHandler {
	return &OperationAuditHandler{service: auditService}
}

func (h *OperationAuditHandler) List(c *gin.Context) {
	filter := &service.OperationAuditFilter{
		ActorEmail:   strings.TrimSpace(c.Query("actor_email")),
		Action:       strings.TrimSpace(c.Query("action")),
		ResourceType: strings.TrimSpace(c.Query("resource_type")),
		ResourceID:   strings.TrimSpace(c.Query("resource_id")),
		Result:       strings.TrimSpace(c.Query("result")),
	}
	if value := strings.TrimSpace(c.Query("before_id")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "Invalid before_id")
			return
		}
		filter.BeforeID = parsed
	}
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > 200 {
			response.BadRequest(c, "Invalid limit")
			return
		}
		filter.Limit = parsed
	}
	if value := strings.TrimSpace(c.Query("actor_user_id")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "Invalid actor_user_id")
			return
		}
		filter.ActorUserID = &parsed
	}
	if value := strings.TrimSpace(c.Query("start_time")); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid start_time")
			return
		}
		filter.StartTime = &parsed
	}
	if value := strings.TrimSpace(c.Query("end_time")); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid end_time")
			return
		}
		filter.EndTime = &parsed
	}
	if filter.StartTime != nil && filter.EndTime != nil && filter.StartTime.After(*filter.EndTime) {
		response.BadRequest(c, "start_time must not be after end_time")
		return
	}
	result, err := h.service.List(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *OperationAuditHandler) PreviewClear(c *gin.Context) {
	preview, err := h.service.PreviewClear(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

func (h *OperationAuditHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid audit log id")
		return
	}
	item, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Clear performs the clear and tail-record insert in one PostgreSQL transaction.
// The route has already consumed a one-time audit_logs.clear step-up token.
func (h *OperationAuditHandler) Clear(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}
	uid := subject.UserID
	trace := &service.OperationAuditLog{
		ActorUserID: &uid,
		ActorEmail:  c.GetString(middleware.ContextKeyAuthEmail),
		AuthMethod:  c.GetString("auth_method"),
		Method:      c.Request.Method,
		Path:        c.FullPath(),
		ClientIP:    middleware.SecurityClientIP(c),
		UAHash:      service.HashUserAgent(c.Request.UserAgent()),
		StatusCode:  200,
	}
	trace.ActorRole, _ = middleware.GetUserRoleFromContext(c)
	if requestID, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok {
		trace.RequestID = requestID
	}
	deleted, err := h.service.ClearAllWithTrace(c.Request.Context(), trace)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	middleware.SkipAudit(c)
	response.Success(c, gin.H{"deleted": deleted})
}
