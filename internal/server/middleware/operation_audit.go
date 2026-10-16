package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type OperationAuditMiddleware gin.HandlerFunc

const (
	auditActionKey       = "operation_audit_action"
	auditResourceTypeKey = "operation_audit_resource_type"
	auditResourceIDKey   = "operation_audit_resource_id"
	auditSummaryKey      = "operation_audit_summary"
	auditSkipKey         = "operation_audit_skip"
	auditCaptureLimit    = int64(64 * 1024)
)

type auditRouteSpec struct {
	Action       string
	ResourceType string
	Critical     bool
}

var auditRouteSpecs = map[string]auditRouteSpec{
	"GET /api/v1/admin/accounts/data":                                     {"admin.accounts.export", "account", true},
	"POST /api/v1/admin/accounts/data":                                    {"admin.accounts.import", "account", false},
	"GET /api/v1/admin/proxies/data":                                      {"admin.proxies.export", "proxy", true},
	"POST /api/v1/admin/proxies/data":                                     {"admin.proxies.import", "proxy", false},
	"GET /api/v1/admin/backups/s3-config":                                 {"admin.backups.s3.read", "backup_s3", true},
	"PUT /api/v1/admin/backups/s3-config":                                 {"admin.backups.s3.update", "backup_s3", true},
	"POST /api/v1/admin/backups":                                          {"admin.backups.create", "backup", true},
	"GET /api/v1/admin/backups/:id/download-url":                          {"admin.backups.download", "backup", true},
	"POST /api/v1/admin/backups/:id/restore":                              {"admin.backups.restore", "backup", true},
	"DELETE /api/v1/admin/backups/:id":                                    {"admin.backups.delete", "backup", true},
	"GET /api/v1/admin/data-management/s3/profiles":                       {"admin.data_management.s3.read", "s3_profile", true},
	"GET /api/v1/admin/data-management/config":                            {"admin.data_management.config.read", "data_management", true},
	"PUT /api/v1/admin/data-management/config":                            {"admin.data_management.config.update", "data_management", true},
	"POST /api/v1/admin/data-management/s3/test":                          {"admin.data_management.s3.test", "s3_profile", true},
	"POST /api/v1/admin/data-management/s3/profiles":                      {"admin.data_management.s3.create", "s3_profile", true},
	"PUT /api/v1/admin/data-management/s3/profiles/:profile_id":           {"admin.data_management.s3.update", "s3_profile", true},
	"DELETE /api/v1/admin/data-management/s3/profiles/:profile_id":        {"admin.data_management.s3.delete", "s3_profile", true},
	"POST /api/v1/admin/data-management/s3/profiles/:profile_id/activate": {"admin.data_management.s3.activate", "s3_profile", true},
	"POST /api/v1/admin/data-management/backups":                          {"admin.data_management.backup.create", "backup", true},
	"POST /api/v1/admin/backups/s3-config/test":                           {"admin.backups.s3.test", "backup_s3", true},
	"POST /api/v1/admin/users":                                            {"admin.users.create", "user", true},
	"PUT /api/v1/admin/users/:id":                                         {"admin.users.update", "user", true},
	"POST /api/v1/admin/groups/:id/duplicate":                             {"admin.groups.duplicate", "group", false},
	"PUT /api/v1/admin/groups/:id/pricing":                                {"admin.groups.pricing.update", "group", false},
	"POST /api/v1/admin/channel-monitors/:id/duplicate":                   {"admin.channel_monitors.duplicate", "channel_monitor", false},
	"POST /api/v1/admin/settings/admin-api-key/reveal":                    {"admin.admin_api_key.reveal", "admin_api_key", true},
	"POST /api/v1/admin/settings/admin-api-key/regenerate":                {"admin.admin_api_key.regenerate", "admin_api_key", true},
	"DELETE /api/v1/admin/settings/admin-api-key":                         {"admin.admin_api_key.delete", "admin_api_key", true},
	"POST /api/v1/admin/gateway-lite/local-api-keys/:id/reveal":           {"admin.gateway_lite_local_api_key.reveal", "gateway_lite_local_api_key", true},
	"POST /api/v1/admin/image-tasks/cleanup":                              {"admin.image_tasks.cleanup", "image_task", true},
	"POST /api/v1/admin/image-tasks/:id/replay":                           {"admin.image_tasks.replay", "image_task", true},
	"PUT /api/v1/admin/image-tasks/config":                                {"admin.image_tasks.config.update", "image_task_config", true},
	"POST /api/v1/user/totp/setup":                                        {service.AuditActionTotpSetup, "totp", true},
	"POST /api/v1/user/totp/enable":                                       {service.AuditActionTotpEnable, "totp", true},
	"POST /api/v1/user/totp/disable":                                      {service.AuditActionTotpDisable, "totp", true},
	"POST /api/v1/user/totp/step-up":                                      {service.AuditActionStepUpVerify, "step_up", false},
}


func SetAuditResource(c *gin.Context, resourceType, resourceID string) {
	c.Set(auditResourceTypeKey, strings.TrimSpace(resourceType))
	c.Set(auditResourceIDKey, strings.TrimSpace(resourceID))
}
func SetAuditSummary(c *gin.Context, summary map[string]any) {
	c.Set(auditSummaryKey, service.SanitizeOperationAuditSummary(summary))
}


func NewOperationAuditMiddleware(auditService *service.OperationAuditService) OperationAuditMiddleware {
	return OperationAuditMiddleware(func(c *gin.Context) {
		routeKey := c.Request.Method + " " + c.FullPath()
		spec, explicit := auditRouteSpecs[routeKey]
		record := explicit || shouldAuditMutation(c.Request.Method)
		if !record {
			c.Next()
			return
		}

		requestSummary := captureAuditFieldSummary(c)
		entry := buildOperationAuditEntry(c, spec, requestSummary)
		start := time.Now()
		criticalID := int64(0)
		if spec.Critical {
			var err error
			criticalID, err = auditService.BeginCritical(c.Request.Context(), entry)
			if err != nil {
				slog.Error("critical operation audit preflight failed", "action", entry.Action, "error", err)
				AbortWithError(c, 503, "AUDIT_LOG_UNAVAILABLE", "Security audit service unavailable")
				return
			}
		}

		c.Next()
		if c.GetBool(auditSkipKey) {
			return
		}
		status := c.Writer.Status()
		result := auditResultFromStatus(status)
		mergeAuditContext(entry, c)
		entry.StatusCode = status
		entry.Result = result
		entry.LatencyMs = time.Since(start).Milliseconds()
		if criticalID > 0 {
			if err := auditService.FinishCritical(c.Request.Context(), criticalID, result, status, time.Since(start), entry.Summary); err != nil {
				// Intent remains pending and preserves who/what/when even if the completion update fails.
				slog.Error("critical operation audit finalize failed", "audit_id", criticalID, "action", entry.Action, "error", err)
			}
			return
		}
		auditService.Record(entry)
	})
}

func shouldAuditMutation(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

func captureAuditFieldSummary(c *gin.Context) map[string]any {
	if c.Request.Body == nil || !shouldAuditMutation(c.Request.Method) {
		return nil
	}
	original := c.Request.Body
	prefix, err := io.ReadAll(io.LimitReader(original, auditCaptureLimit+1))
	if err != nil {
		return map[string]any{"body": "unreadable_omitted"}
	}
	c.Request.Body = &auditRestoredBody{Reader: io.MultiReader(bytes.NewReader(prefix), original), closer: original}
	summary := service.SummarizeAuditJSON(prefix)
	if int64(len(prefix)) > auditCaptureLimit {
		if summary == nil {
			summary = map[string]any{}
		}
		summary["field_scan_truncated"] = true
	}
	return summary
}

type auditRestoredBody struct {
	io.Reader
	closer io.Closer
}

func (b *auditRestoredBody) Close() error { return b.closer.Close() }

func buildOperationAuditEntry(c *gin.Context, spec auditRouteSpec, summary map[string]any) *service.OperationAuditLog {
	entry := &service.OperationAuditLog{
		CreatedAt:    time.Now().UTC(),
		Action:       spec.Action,
		ResourceType: spec.ResourceType,
		Result:       service.AuditResultPending,
		Method:       c.Request.Method,
		Path:         c.FullPath(),
		ClientIP:     SecurityClientIP(c),
		UAHash:       service.HashUserAgent(c.Request.UserAgent()),
		Summary:      summary,
	}
	if entry.Path == "" {
		entry.Path = c.Request.URL.Path
	}
	if entry.Action == "" {
		entry.Action = deriveOperationAuditAction(entry.Method, entry.Path)
	}
	if entry.ResourceType == "" {
		entry.ResourceType = deriveOperationResourceType(entry.Path)
	}
	entry.ResourceID = auditResourceIDFromParams(c)
	if requestID, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok {
		entry.RequestID = requestID
	}
	if subject, ok := GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		id := subject.UserID
		entry.ActorUserID = &id
	}
	entry.ActorEmail = c.GetString(ContextKeyAuthEmail)
	entry.AuthMethod = c.GetString("auth_method")
	entry.ActorRole, _ = GetUserRoleFromContext(c)
	return entry
}

func mergeAuditContext(entry *service.OperationAuditLog, c *gin.Context) {
	if value := c.GetString(auditActionKey); value != "" {
		entry.Action = value
	}
	if value := c.GetString(auditResourceTypeKey); value != "" {
		entry.ResourceType = value
	}
	if value := c.GetString(auditResourceIDKey); value != "" {
		entry.ResourceID = value
	}
	if value, ok := c.Get(auditSummaryKey); ok {
		if summary, ok := value.(map[string]any); ok {
			if entry.Summary == nil {
				entry.Summary = map[string]any{}
			}
			for key, item := range summary {
				entry.Summary[key] = item
			}
			entry.Summary = service.SanitizeOperationAuditSummary(entry.Summary)
		}
	}
}

func auditResultFromStatus(status int) string {
	if status == 401 || status == 403 {
		return service.AuditResultDenied
	}
	if status >= 400 {
		return service.AuditResultFailure
	}
	return service.AuditResultSuccess
}

func deriveOperationAuditAction(method, path string) string {
	parts := make([]string, 0, 4)
	for _, part := range strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/v1/"), "/"), "/") {
		if part == "" || strings.HasPrefix(part, ":") {
			continue
		}
		parts = append(parts, strings.ReplaceAll(part, "-", "_"))
	}
	verb := map[string]string{"POST": "create", "PUT": "update", "PATCH": "update", "DELETE": "delete", "GET": "read"}[method]
	if verb == "" {
		verb = strings.ToLower(method)
	}
	return strings.Join(append(parts, verb), ".")
}

func deriveOperationResourceType(path string) string {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/v1/admin/"), "/")
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "ops/runtime/") || lower == "ops/runtime":
		return "runtime_config"
	case strings.Contains(lower, "content-review") || strings.Contains(lower, "prompt-audit") || strings.Contains(lower, "keyword"):
		return "security_policy"
	case strings.HasPrefix(lower, "settings/") || lower == "settings":
		return "security_setting"
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 {
		return ""
	}
	resource := strings.ReplaceAll(parts[0], "-", "_")
	if singular, ok := map[string]string{
		"accounts": "account", "groups": "group", "proxies": "proxy",
		"users": "user", "backups": "backup", "channel_monitors": "channel_monitor",
	}[resource]; ok {
		return singular
	}
	return resource
}

func auditResourceIDFromParams(c *gin.Context) string {
	for _, key := range []string{"id", "job_id", "profile_id", "user_id"} {
		if value := strings.TrimSpace(c.Param(key)); value != "" {
			return value
		}
	}
	return ""
}
