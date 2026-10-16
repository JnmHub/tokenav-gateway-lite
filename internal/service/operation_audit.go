package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrOperationAuditNotFound = infraerrors.NotFound("OPERATION_AUDIT_NOT_FOUND", "operation audit log not found")

// 注意：认证方式与通用审计动作常量已由 audit_log.go（我方实现）定义，
// 此处只保留 operation_audit 独有的 gateway_lite_admin_key 与结果态常量，避免重复定义。
const (
	AuditAuthMethodGatewayAdminKey = "gateway_lite_admin_key"

	AuditResultPending = "pending"
	AuditResultSuccess = "success"
	AuditResultFailure = "failure"
	AuditResultDenied  = "denied"

	AuditActionTotpSetup   = "user.totp.setup"
	AuditActionTotpEnable  = "user.totp.enable"
	AuditActionTotpDisable = "user.totp.disable"
)

// OperationAuditLog is deliberately value-poor: field names and redacted state only.
type OperationAuditLog struct {
	ID           int64          `json:"id"`
	CreatedAt    time.Time      `json:"created_at"`
	ActorUserID  *int64         `json:"actor_user_id,omitempty"`
	ActorEmail   string         `json:"actor_email,omitempty"`
	ActorRole    string         `json:"actor_role,omitempty"`
	AuthMethod   string         `json:"auth_method,omitempty"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type,omitempty"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Result       string         `json:"result"`
	Method       string         `json:"method,omitempty"`
	Path         string         `json:"path,omitempty"`
	RequestID    string         `json:"request_id,omitempty"`
	ClientIP     string         `json:"client_ip,omitempty"`
	UAHash       string         `json:"ua_hash,omitempty"`
	StatusCode   int            `json:"status_code"`
	LatencyMs    int64          `json:"latency_ms"`
	Summary      map[string]any `json:"summary,omitempty"`
	Extra        map[string]any `json:"extra,omitempty"`
}

type OperationAuditFilter struct {
	BeforeID     int64
	Limit        int
	ActorUserID  *int64
	ActorEmail   string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	StartTime    *time.Time
	EndTime      *time.Time
}

type OperationAuditList struct {
	Items      []*OperationAuditLog `json:"items"`
	NextCursor int64                `json:"next_cursor,omitempty"`
}

// OperationAuditClearPreview lets the UI show the exact blast radius before
// the administrator consumes a step-up token and irreversibly clears history.
type OperationAuditClearPreview struct {
	Rows     int64      `json:"rows"`
	OldestAt *time.Time `json:"oldest_at,omitempty"`
	NewestAt *time.Time `json:"newest_at,omitempty"`
}

type OperationAuditRepository interface {
	Insert(ctx context.Context, entry *OperationAuditLog) (int64, error)
	BatchInsert(ctx context.Context, entries []*OperationAuditLog) (int64, error)
	UpdateResult(ctx context.Context, id int64, result string, statusCode int, latencyMs int64, summary map[string]any) error
	List(ctx context.Context, filter *OperationAuditFilter) (*OperationAuditList, error)
	GetByID(ctx context.Context, id int64) (*OperationAuditLog, error)
	DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error)
	ClearAllWithTrace(ctx context.Context, trace *OperationAuditLog) (int64, error)
}

// OperationAuditPreviewRepository is optional to keep lightweight test and
// compatibility repositories source-compatible. The production PostgreSQL
// repository implements it.
type OperationAuditPreviewRepository interface {
	PreviewClear(ctx context.Context) (*OperationAuditClearPreview, error)
}

var sensitiveAuditFieldTokens = []string{
	"secret", "password", "token", "credential", "authorization", "cookie", "api_key", "apikey", "private_key", "access_key",
}

var allowedOperationAuditExtraFields = map[string]struct{}{
	"binding": {}, "configured": {}, "count": {}, "mode": {},
	"reason": {}, "source": {}, "version": {},
}

const maxOperationAuditExtraStringBytes = 128

const (
	maxOperationAuditFieldNameBytes = 64
	maxOperationAuditFields         = 128
)

var allowedOperationAuditStates = map[string]struct{}{
	"changed": {}, "configured": {}, "cleared_or_omitted": {},
}

var allowedOperationAuditBodyStates = map[string]struct{}{
	"non_json_omitted": {}, "non_object_omitted": {}, "unreadable_omitted": {},
}

var allowedOperationAuditSummaryLabels = map[string]map[string]struct{}{
	"mode": {
		"off": {}, "observe": {}, "enforce": {},
		"5m": {}, "15m": {}, "30m": {}, "60m": {},
	},
	"binding": {"mismatch": {}},
}

// SanitizeOperationAuditExtra prevents future handlers from turning the
// extensibility column into an arbitrary request/response dump. Only a small
// value-only vocabulary is accepted; maps, slices and long strings are dropped.
func SanitizeOperationAuditExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(extra))
	for rawKey, value := range extra {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if _, ok := allowedOperationAuditExtraFields[key]; !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			typed = strings.TrimSpace(typed)
			if typed == "" || len(typed) > maxOperationAuditExtraStringBytes {
				continue
			}
			out[key] = typed
		case bool, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, float32, float64:
			out[key] = typed
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SanitizeOperationAuditSummary enforces one value-poor schema for every
// writer and reader. In particular, handler-provided maps cannot smuggle a
// Key, Refresh Token, Cookie, proxy password or request body into JSONB.
func SanitizeOperationAuditSummary(summary map[string]any) map[string]any {
	if len(summary) == 0 {
		return nil
	}
	out := make(map[string]any, 8)
	if fields := sanitizeAuditFieldList(summary["fields"]); len(fields) > 0 {
		out["fields"] = fields
	}
	if states := sanitizeAuditStates(summary["states"]); len(states) > 0 {
		out["states"] = states
	}
	if value, ok := summary["body"].(string); ok {
		value = strings.TrimSpace(value)
		if _, allowed := allowedOperationAuditBodyStates[value]; allowed {
			out["body"] = value
		}
	}
	for _, key := range []string{"field_scan_truncated", "configured"} {
		if value, ok := summary[key].(bool); ok {
			out[key] = value
		}
	}
	for _, key := range []string{"mode", "binding"} {
		if value, ok := summary[key].(string); ok {
			value = strings.ToLower(strings.TrimSpace(value))
			if _, allowed := allowedOperationAuditSummaryLabels[key][value]; allowed {
				out[key] = value
			}
		}
	}
	for _, key := range []string{"count", "deleted_rows"} {
		if value, ok := nonNegativeAuditInteger(summary[key]); ok {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeAuditFieldList(value any) []string {
	var raw []string
	switch typed := value.(type) {
	case []string:
		raw = typed
	case []any:
		raw = make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	}
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, min(len(raw), maxOperationAuditFields))
	result := make([]string, 0, min(len(raw), maxOperationAuditFields))
	for _, name := range raw {
		name = sanitizeAuditFieldName(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
		if len(result) == maxOperationAuditFields {
			break
		}
	}
	sort.Strings(result)
	return result
}

func sanitizeAuditStates(value any) map[string]string {
	result := make(map[string]string)
	add := func(rawKey, rawState string) {
		if len(result) >= maxOperationAuditFields {
			return
		}
		state := strings.TrimSpace(rawState)
		if _, ok := allowedOperationAuditStates[state]; !ok {
			return
		}
		if key := sanitizeAuditFieldName(rawKey); key != "" {
			result[key] = state
		}
	}
	switch typed := value.(type) {
	case map[string]string:
		for key, state := range typed {
			add(key, state)
		}
	case map[string]any:
		for key, state := range typed {
			if text, ok := state.(string); ok {
				add(key, text)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func sanitizeAuditFieldName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= maxOperationAuditFieldNameBytes && auditFieldNameIsSafe(value) && !auditFieldNameLooksLikeCredential(value) {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	// A short, irreversible fingerprint preserves correlation without keeping
	// the attacker-controlled field name itself.
	return fmt.Sprintf("field_sha256_%x", digest[:8])
}

func auditFieldNameIsSafe(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func auditFieldNameLooksLikeCredential(value string) bool {
	lower := strings.ToLower(value)
	for _, prefix := range []string{"bearer-", "bearer_", "sk-", "sk_", "sess-", "sess_", "eyj"} {
		if strings.HasPrefix(lower, prefix) && len(value) >= 16 {
			return true
		}
	}
	return false
}

func nonNegativeAuditInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), typed >= 0
	case int8:
		return int64(typed), typed >= 0
	case int16:
		return int64(typed), typed >= 0
	case int32:
		return int64(typed), typed >= 0
	case int64:
		return typed, typed >= 0
	case uint:
		if uint64(typed) <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
	case float64:
		integer := int64(typed)
		if typed >= 0 && float64(integer) == typed {
			return integer, true
		}
	}
	return 0, false
}

// SummarizeAuditJSON returns only sorted field names and redacted state labels.
func SummarizeAuditJSON(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{"body": "non_json_omitted"}
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return map[string]any{"body": "non_object_omitted"}
	}
	fields := make([]string, 0, len(obj))
	states := make(map[string]string, len(obj))
	for key, fieldValue := range obj {
		name := sanitizeAuditFieldName(key)
		if name == "" {
			continue
		}
		fields = append(fields, name)
		state := "changed"
		lower := strings.ToLower(strings.TrimSpace(key))
		for _, token := range sensitiveAuditFieldTokens {
			if strings.Contains(lower, token) {
				state = "configured"
				if fieldValue == nil || strings.TrimSpace(asAuditString(fieldValue)) == "" {
					state = "cleared_or_omitted"
				}
				break
			}
		}
		states[name] = state
	}
	sort.Strings(fields)
	if len(fields) == 0 {
		return nil
	}
	return SanitizeOperationAuditSummary(map[string]any{"fields": fields, "states": states})
}

func asAuditString(v any) string {
	s, _ := v.(string)
	return s
}
