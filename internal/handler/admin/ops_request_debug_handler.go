package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/debugschema"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

type requestDebugUpdate struct {
	Preset     string `json:"preset"`
	MaxEntries int    `json:"max_entries"`
	MaxRecords int    `json:"max_records"`
	Scope      struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	} `json:"scope"`
	ScopeType string `json:"scope_type"`
	GroupID   any    `json:"group_id"`
	APIKeyID  any    `json:"api_key_id"`
}

type fullRequestDebugUpdate struct {
	Preset        string `json:"preset"`
	APIKeyID      int64  `json:"api_key_id"`
	KeyID         int64  `json:"key_id"`
	APIKeyRef     string `json:"api_key_ref"`
	MaxRecords    int    `json:"max_records"`
	MaxBodyBytes  int    `json:"max_body_bytes"`
	MaxTotalBytes int    `json:"max_total_bytes"`
}

func (h *OpsHandler) GetRequestDebug(c *gin.Context) {
	response.Success(c, debugschema.DefaultController().Status(time.Now()))
}

func (h *OpsHandler) UpdateRequestDebug(c *gin.Context) {
	var req requestDebugUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	preset := strings.ToLower(strings.TrimSpace(req.Preset))
	if preset == "" {
		preset = "5m"
	}
	durations := map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute}
	d, ok := durations[preset]
	if !ok {
		response.BadRequest(c, "preset must be one of 5m, 15m, 30m")
		return
	}
	maxEntries := req.MaxEntries
	if req.MaxRecords > 0 {
		maxEntries = req.MaxRecords
	}
	scope := debugschema.Scope{Type: req.Scope.Type, Value: stringifyScopeValue(req.Scope.Value)}
	if strings.TrimSpace(scope.Type) == "" {
		scope.Type = strings.TrimSpace(req.ScopeType)
	}
	if strings.TrimSpace(scope.Value) == "" {
		if strings.EqualFold(scope.Type, "group") {
			scope.Value = stringifyScopeValue(req.GroupID)
		} else if strings.EqualFold(scope.Type, "api_key") {
			scope.Value = stringifyScopeValue(req.APIKeyID)
		}
	}
	scope = debugschema.NormalizeScope(scope)
	debugschema.DefaultController().EnableWithScope(time.Now().Add(d), maxEntries, scope)
	middleware.SetAuditAction(c, "admin.runtime.request_debug.enable")
	middleware.SetAuditResource(c, "runtime_config", "request_debug")
	middleware.SetAuditSummary(c, map[string]any{"preset": preset, "max_records": maxEntries})
	response.Success(c, debugschema.DefaultController().Status(time.Now()))
}

func (h *OpsHandler) DisableRequestDebug(c *gin.Context) {
	debugschema.DefaultController().Disable()
	middleware.SetAuditAction(c, "admin.runtime.request_debug.disable")
	middleware.SetAuditResource(c, "runtime_config", "request_debug")
	response.Success(c, debugschema.DefaultController().Status(time.Now()))
}
func (h *OpsHandler) ListRequestDebugRecords(c *gin.Context) {
	items := debugschema.DefaultController().List()
	records := make([]gin.H, 0, len(items))
	for _, item := range items {
		reqHeaders := make([]string, 0, len(item.RequestHeaders))
		for _, h := range item.RequestHeaders {
			reqHeaders = append(reqHeaders, h.Name)
		}
		respHeaders := make([]string, 0, len(item.ResponseHeaders))
		for _, h := range item.ResponseHeaders {
			respHeaders = append(respHeaders, h.Name)
		}
		records = append(records, gin.H{"id": item.ID, "created_at": item.CreatedAt, "method": item.Method, "path": item.Path, "request_id": item.RequestID, "api_key_id": item.APIKeyID, "api_key_ref": item.APIKeyRef, "group_id": item.GroupID, "group_name": item.GroupName, "request_headers": reqHeaders, "request_schema": item.RequestBody, "response_status": item.ResponseStatus, "response_headers": respHeaders, "response_schema": item.ResponseBody, "duration_ms": item.DurationMs})
	}
	response.Success(c, gin.H{"items": records, "records": records})
}

func stringifyScopeValue(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return ""
	}
}
func (h *OpsHandler) ClearRequestDebugRecords(c *gin.Context) {
	debugschema.DefaultController().Clear()
	response.Success(c, debugschema.DefaultController().Status(time.Now()))
}

func (h *OpsHandler) GetFullRequestDebug(c *gin.Context) {
	response.Success(c, debugschema.DefaultFullCaptureController().Status(time.Now()))
}

func (h *OpsHandler) UpdateFullRequestDebug(c *gin.Context) {
	var req fullRequestDebugUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	keyID := req.APIKeyID
	if keyID <= 0 {
		keyID = req.KeyID
	}
	if keyID <= 0 && strings.TrimSpace(req.APIKeyRef) == "" {
		response.BadRequest(c, "api_key_id is required")
		return
	}
	preset := strings.ToLower(strings.TrimSpace(req.Preset))
	if preset == "" {
		preset = "15m"
	}
	durations := map[string]time.Duration{"5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute}
	d, ok := durations[preset]
	if !ok {
		response.BadRequest(c, "preset must be one of 5m, 15m, 30m")
		return
	}
	debugschema.DefaultFullCaptureController().Enable(time.Now().Add(d), keyID, req.APIKeyRef, req.MaxRecords, req.MaxBodyBytes, req.MaxTotalBytes)
	middleware.SetAuditAction(c, "admin.runtime.request_debug.full_enable")
	middleware.SetAuditResource(c, "runtime_config", "request_debug_full")
	response.Success(c, debugschema.DefaultFullCaptureController().Status(time.Now()))
}

func (h *OpsHandler) DisableFullRequestDebug(c *gin.Context) {
	debugschema.DefaultFullCaptureController().Disable()
	middleware.SetAuditAction(c, "admin.runtime.request_debug.full_disable")
	middleware.SetAuditResource(c, "runtime_config", "request_debug_full")
	response.Success(c, debugschema.DefaultFullCaptureController().Status(time.Now()))
}

func (h *OpsHandler) ListFullRequestDebugRecords(c *gin.Context) {
	items := debugschema.DefaultFullCaptureController().List()
	records := make([]gin.H, 0, len(items))
	for _, item := range items {
		records = append(records, gin.H{"id": item.ID, "created_at": item.CreatedAt, "duration_ms": item.DurationMs, "method": item.Method, "path": item.Path, "request_id": item.RequestID, "api_key_id": item.APIKeyID, "api_key_ref": item.APIKeyRef, "group_id": item.GroupID, "group_name": item.GroupName, "request_headers": item.RequestHeaders, "request_body": debugschema.PayloadForJSON(item.RequestBody, item.RequestContentType, item.RequestTruncated), "response_status": item.ResponseStatus, "response_headers": item.ResponseHeaders, "response_body": debugschema.PayloadForJSON(item.ResponseBody, item.ResponseContentType, item.ResponseTruncated)})
	}
	response.Success(c, gin.H{"items": records, "records": records})
}

func (h *OpsHandler) ClearFullRequestDebugRecords(c *gin.Context) {
	debugschema.DefaultFullCaptureController().Clear()
	response.Success(c, debugschema.DefaultFullCaptureController().Status(time.Now()))
}
