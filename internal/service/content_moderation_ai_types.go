package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

const (
	ContentModerationKeywordModeAIOnly  = "ai_only"
	ContentModerationModeShadow         = "shadow"
	ContentModerationAIAdapterChat      = "deepseek_chat"
	ContentModerationAIAdapterResponses = "deepseek_responses"
)

// 专用密钥和外发授权独立保存，避免切换 AI 审查时默默复用其他服务凭据。
type ContentModerationAIConfig struct {
	Adapter             string `json:"adapter"`
	BaseURL             string `json:"base_url"`
	Model               string `json:"model"`
	ModelRevision       string `json:"model_revision"`
	APIKey              string `json:"api_key,omitempty"`
	ExternalDataConsent bool   `json:"external_data_consent"`
	PolicyVersion       string `json:"policy_version"`
	Policy              string `json:"policy"`
	PenaltiesEnabled    bool   `json:"penalties_enabled"`
	// 关闭时不解码、不发送图片，只记录未覆盖；外链图片始终不下载。
	ImageReviewEnabled bool `json:"image_review_enabled"`
}

type ContentModerationAIConfigView struct {
	ContentModerationAIConfig
	APIKeyConfigured  bool                            `json:"api_key_configured"`
	APIKeyMasked      string                          `json:"api_key_masked"`
	Capabilities      []ContentModerationAICapability `json:"capabilities"`
	ConfigFingerprint string                          `json:"config_fingerprint"`
}

// Test metadata is process-local evidence, never a manually editable revision.
// Restarting the process resets it to not_tested; saved secrets are not exposed.
type ContentModerationAITestStatus struct {
	Status             string     `json:"status"`
	ConfigFingerprint  string     `json:"config_fingerprint"`
	CaseID             string     `json:"case_id,omitempty"`
	Verdict            string     `json:"verdict,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	ProviderHTTPStatus int        `json:"provider_http_status,omitempty"`
	CoverageComplete   bool       `json:"coverage_complete"`
	TestedAt           *time.Time `json:"tested_at,omitempty"`
}

type ContentModerationAIReadinessCheck struct {
	Code    string `json:"code"`
	Ready   bool   `json:"ready"`
	Message string `json:"message"`
}

type ContentModerationAIReadiness struct {
	State  string                              `json:"state"`
	Ready  bool                                `json:"ready"`
	Checks []ContentModerationAIReadinessCheck `json:"checks"`
}

type UpdateContentModerationAIConfigInput struct {
	Adapter             *string `json:"adapter"`
	BaseURL             *string `json:"base_url"`
	Model               *string `json:"model"`
	ModelRevision       *string `json:"model_revision"`
	APIKey              *string `json:"api_key"`
	ClearAPIKey         bool    `json:"clear_api_key"`
	ExternalDataConsent *bool   `json:"external_data_consent"`
	PolicyVersion       *string `json:"policy_version"`
	Policy              *string `json:"policy"`
	PenaltiesEnabled    *bool   `json:"penalties_enabled"`
	ImageReviewEnabled  *bool   `json:"image_review_enabled"`
}

// 图片与文本一起参与整体判定；图片 ID 只用于标识来源，不代表独立判定结果。
type ContentModerationAIImage struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Role      string `json:"role"`
	MediaType string `json:"media_type"`
	// 图片字节仅用于送审，不写入日志或响应。
	DataURL string `json:"-"`
	Bytes   int    `json:"bytes"`
}

// 每个原文本字段保留一段完整文本，起止位置只描述原字段，不做切片或重叠。
type ContentModerationAIFragment struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Role      string `json:"role"`
	Source    string `json:"source"`
	StartRune int    `json:"start_rune"`
	EndRune   int    `json:"end_rune"`
	Text      string `json:"text"`
}

type ContentModerationAICanonicalInput struct {
	Protocol  string                        `json:"protocol"`
	InputHash string                        `json:"input_hash"`
	Fragments []ContentModerationAIFragment `json:"fragments"`
	Images    []ContentModerationAIImage    `json:"images,omitempty"`
	// 未知扩展、远程历史和未选媒体只记录未覆盖，不能宣称这些内容已审查。
	UncoveredFields []string `json:"uncovered_fields,omitempty"`
	ForwardBody     []byte   `json:"-"`
	TextRunes       int      `json:"text_runes"`
}

// 模型只返回一次整体判定，不能反向伪造每段或每张图片的独立结论。
type ContentModerationAIResult struct {
	PolicyVersion string   `json:"policy_version"`
	Verdict       string   `json:"verdict"`
	Categories    []string `json:"categories"`
	Reason        string   `json:"reason"`
}

type ContentModerationAICoverage struct {
	Complete        bool     `json:"complete"`
	InputHash       string   `json:"input_hash"`
	FragmentCount   int      `json:"fragment_count"`
	ImageCount      int      `json:"image_count"`
	ReviewedCount   int      `json:"reviewed_count"`
	TextRunes       int      `json:"text_runes"`
	MediaSupported  bool     `json:"media_supported"`
	UncoveredFields []string `json:"uncovered_fields,omitempty"`
}

type ContentModerationAICapability struct {
	Protocol  string `json:"protocol"`
	Supported bool   `json:"supported"`
	Scope     string `json:"scope"`
	Reason    string `json:"reason"`
}

type ContentModerationAIMetrics struct {
	ConfigSaved         bool                            `json:"config_saved"`
	ConfigFingerprint   string                          `json:"config_fingerprint"`
	ServiceEndpoint     string                          `json:"service_endpoint"`
	Test                ContentModerationAITestStatus   `json:"test"`
	Production          ContentModerationAIReadiness    `json:"production"`
	ProductionChecked   int64                           `json:"production_checked"`
	ProductionAllowed   int64                           `json:"production_allowed"`
	ProductionBlocked   int64                           `json:"production_blocked"`
	ProductionErrors    int64                           `json:"production_errors"`
	Configured          bool                            `json:"configured"`
	ExternalDataConsent bool                            `json:"external_data_consent"`
	StoreReady          bool                            `json:"store_ready"`
	Adapter             string                          `json:"adapter"`
	Model               string                          `json:"model"`
	PolicyVersion       string                          `json:"policy_version"`
	StateScope          string                          `json:"state_scope"`
	Active              int                             `json:"active"`
	Checked             int64                           `json:"checked"`
	Allowed             int64                           `json:"allowed"`
	Blocked             int64                           `json:"blocked"`
	Errors              int64                           `json:"errors"`
	ShadowChecked       int64                           `json:"shadow_checked"`
	TotalCalls          int64                           `json:"total_calls"`
	AvgLatencyMS        int64                           `json:"avg_latency_ms"`
	LastLatencyMS       int64                           `json:"last_latency_ms"`
	LastErrorCode       string                          `json:"last_error_code"`
	PenaltyPending      int64                           `json:"penalty_pending"`
	PenaltyDead         int64                           `json:"penalty_dead"`
	Capabilities        []ContentModerationAICapability `json:"capabilities"`
}

type TestContentModerationAIInput struct {
	CaseID            string `json:"case_id"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
}

type ContentModerationAIStoreStats struct {
	PenaltyPending int64
	PenaltyDead    int64
}

// 存储只负责违规处罚的可靠投递；审查不额外预占业务额度或维护运营预算。
type ContentModerationAIStore interface {
	EnqueuePenalty(context.Context, gatewaylite.ContentModerationPenaltyRequest) error
	ClaimPenalty(context.Context, time.Time) (*ContentModerationAIPenaltyEvent, error)
	FinishPenalty(context.Context, ContentModerationAIPenaltyEvent, error, bool) error
	Stats(context.Context, string, time.Time) (ContentModerationAIStoreStats, error)
}

type ContentModerationAIPenaltyEvent struct {
	Request  gatewaylite.ContentModerationPenaltyRequest
	Token    string
	Attempts int
}

type ContentModerationAISQLDB interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type contentModerationAIError struct {
	Code               string
	Status             int
	ProviderHTTPStatus int
	Retryable          bool
}

func (e *contentModerationAIError) Error() string { return e.Code }
func aiError(code string, status int) error {
	return &contentModerationAIError{Code: code, Status: status}
}

// strictJSON rejects duplicate keys before the same bytes enter either the
// gateway converter or the AI result decoder. It also validates trailing data.
func strictJSON(raw []byte) (any, error) { return parseContentModerationAIStrictJSON(raw) }

func aiJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
