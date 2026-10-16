package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	ContentModerationModeOff      = "off"
	ContentModerationModeObserve  = "observe"
	ContentModerationModePreBlock = "pre_block"

	contentModerationAPIKeysModeAppend  = "append"
	contentModerationAPIKeysModeReplace = "replace"

	ContentModerationActionAllow          = "allow"
	ContentModerationActionBlock          = "block"
	ContentModerationActionHashBlock      = "hash_block"
	ContentModerationActionKeywordBlock   = "keyword_block"
	ContentModerationActionKeywordAudit   = "keyword_audit"
	ContentModerationActionKeywordAllow   = "keyword_allow_original"
	ContentModerationActionKeywordReroute = "keyword_reroute"
	ContentModerationActionLanguageBlock  = "language_block"
	ContentModerationActionError          = "error"
	ContentModerationActionCyberPolicy    = "cyber_policy" // cyber_policy 硬阻断的风控日志 action（封号计数排除按此值过滤）

	contentModerationKeywordCategory = "keyword"

	ContentModerationKeywordModeKeywordOnly   = "keyword_only"
	ContentModerationKeywordModeKeywordAndAPI = "keyword_and_api"
	ContentModerationKeywordModeAPIOnly       = "api_only"

	ContentModerationModelFilterAll     = "all"
	ContentModerationModelFilterInclude = "include"
	ContentModerationModelFilterExclude = "exclude"

	ContentModerationProtocolAnthropicMessages = "anthropic_messages"
	ContentModerationProtocolOpenAIResponses   = "openai_responses"
	ContentModerationProtocolOpenAIChat        = "openai_chat_completions"
	ContentModerationProtocolGemini            = "gemini"
	ContentModerationProtocolOpenAIImages      = "openai_images"

	defaultContentModerationBaseURL   = "https://api.openai.com"
	defaultContentModerationModel     = "omni-moderation-latest"
	defaultContentModerationTimeoutMS = 3000
	maxContentModerationTimeoutMS     = 30000
	maxModerationInputRunes           = 12000
	maxModerationExcerptRunes         = 240

	defaultContentModerationWorkerCount           = 4
	maxContentModerationWorkerCount               = 32
	defaultContentModerationQueueSize             = 32768
	maxContentModerationQueueSize                 = 100000
	defaultContentModerationBanThreshold          = 10
	defaultContentModerationPermanentBanThreshold = 20
	defaultContentModerationViolationWindowHours  = 720
	defaultContentModerationBlockHTTPStatus       = http.StatusForbidden
	defaultContentModerationBlockMessage          = "内容审计命中风险规则，请调整输入后重试"
	defaultContentModerationRetryCount            = 2
	maxContentModerationRetryCount                = 5
	defaultContentModerationHitRetentionDays      = 180
	defaultContentModerationNonHitRetentionDays   = 3
	maxContentModerationRetentionDays             = 3650
	maxContentModerationNonHitRetentionDays       = 3
	contentModerationKeyRateLimitFreezeDuration   = time.Minute
	contentModerationKeyAuthFreezeDuration        = 10 * time.Minute
	contentModerationKeyHTTPErrorFreezeDuration   = 10 * time.Second
	maxContentModerationInputImages               = 1
	maxContentModerationTestImages                = maxContentModerationInputImages
	maxContentModerationTestImageBytes            = 8 * 1024 * 1024
	maxContentModerationTestImageDataURLBytes     = 12 * 1024 * 1024
	maxContentModerationBlockedKeywords           = 10000
	maxContentModerationBlockedKeywordRunes       = 200
	maxContentModerationKeywordRulePacks          = 64
	maxContentModerationModelFilterModels         = 1000
	maxContentModerationModelFilterRunes          = 200

	contentModerationRuntimeCacheTTL       = time.Second
	contentModerationRuntimeRefreshTimeout = 3 * time.Second
	contentModerationCleanupInterval       = 24 * time.Hour
	contentModerationCleanupTimeout        = 30 * time.Minute
	contentModerationCleanupDelay          = 5 * time.Minute

	// worker 数按默认值起、由 workerSupervisor 周期补齐；周期取 15s，
	// 让运维调大 worker_count 后既不必重启、也不会等太久。
	contentModerationWorkerReconcileInterval = 15 * time.Second
	contentModerationWorkerReconcileTimeout  = 5 * time.Second
)

var contentModerationCategoryOrder = []string{
	"harassment",
	"harassment/threatening",
	"hate",
	"hate/threatening",
	"illicit",
	"illicit/violent",
	"self-harm",
	"self-harm/intent",
	"self-harm/instructions",
	"sexual",
	"sexual/minors",
	"violence",
	"violence/graphic",
}

func ContentModerationDefaultThresholds() map[string]float64 {
	return map[string]float64{
		"harassment":             0.98,
		"harassment/threatening": 0.90,
		"hate":                   0.65,
		"hate/threatening":       0.65,
		"illicit":                0.95,
		"illicit/violent":        0.95,
		"self-harm":              0.65,
		"self-harm/intent":       0.85,
		"self-harm/instructions": 0.65,
		"sexual":                 0.65,
		"sexual/minors":          0.65,
		"violence":               0.95,
		"violence/graphic":       0.95,
	}
}

func ContentModerationCategories() []string {
	out := make([]string, len(contentModerationCategoryOrder))
	copy(out, contentModerationCategoryOrder)
	return out
}

type ContentModerationConfig struct {
	AI                            ContentModerationAIConfig                  `json:"ai"`
	Enabled                       bool                                       `json:"enabled"`
	Mode                          string                                     `json:"mode"`
	BaseURL                       string                                     `json:"base_url"`
	Model                         string                                     `json:"model"`
	APIKey                        string                                     `json:"api_key,omitempty"`
	APIKeys                       []string                                   `json:"api_keys,omitempty"`
	TimeoutMS                     int                                        `json:"timeout_ms"`
	SampleRate                    int                                        `json:"sample_rate"`
	AllGroups                     bool                                       `json:"all_groups"`
	GroupIDs                      []int64                                    `json:"group_ids"`
	RecordNonHits                 bool                                       `json:"record_non_hits"`
	Thresholds                    map[string]float64                         `json:"thresholds"`
	WorkerCount                   int                                        `json:"worker_count"`
	QueueSize                     int                                        `json:"queue_size"`
	BlockStatus                   int                                        `json:"block_status"`
	BlockMessage                  string                                     `json:"block_message"`
	EmailOnHit                    bool                                       `json:"email_on_hit"`
	AutoBanEnabled                bool                                       `json:"auto_ban_enabled"`
	BanThreshold                  int                                        `json:"ban_threshold"`
	DeductBalanceOnBlockEnabled   bool                                       `json:"deduct_balance_on_block_enabled"`
	DeductBalanceOnBlockAmount    float64                                    `json:"deduct_balance_on_block_amount"`
	PermanentBanEnabled           bool                                       `json:"permanent_ban_enabled"`
	PermanentBanThreshold         int                                        `json:"permanent_ban_threshold"`
	ViolationWindowHours          int                                        `json:"violation_window_hours"`
	RetryCount                    int                                        `json:"retry_count"`
	HitRetentionDays              int                                        `json:"hit_retention_days"`
	NonHitRetentionDays           int                                        `json:"non_hit_retention_days"`
	PreHashCheckEnabled           bool                                       `json:"pre_hash_check_enabled"`
	BlockedKeywords               []string                                   `json:"blocked_keywords"`
	KeywordBlockingMode           string                                     `json:"keyword_blocking_mode"`
	KeywordRulePacks              []ContentModerationKeywordRulePack         `json:"keyword_rule_packs"`
	KeywordRulePackOverrides      []ContentModerationKeywordRulePackOverride `json:"keyword_rule_pack_overrides,omitempty"`
	KeywordCatalogOverrideVersion int                                        `json:"keyword_catalog_override_version,omitempty"`
	ModelFilter                   ContentModerationModelFilter               `json:"model_filter"`
	PolicyVersion                 int                                        `json:"policy_version"`
	LocalKeywordOnly              bool                                       `json:"local_keyword_only"`
	AllowedLanguages              []string                                   `json:"allowed_languages"`
	ScanLatestUserInput           bool                                       `json:"scan_latest_user_input"`
	ScanNewSkillContent           bool                                       `json:"scan_new_skill_content"`
	ScanNewToolOutput             bool                                       `json:"scan_new_tool_output"`
	ScanTopLevelInstructions      bool                                       `json:"scan_top_level_instructions"`
	KeywordGroupRoutingEnabled    bool                                       `json:"keyword_group_routing_enabled"`
	// CyberPolicyExcludeFromBanCount 为 true 时，cyber_policy 命中不参与自动封号计数：
	// 当次不判定封号，且历史 cyber 行在 CountFlaggedByUserSince 中被排除。
	// 默认 false（计入，与历史行为一致；旧配置 JSON 无此字段时反序列化为 false）。
	CyberPolicyExcludeFromBanCount bool `json:"cyber_policy_exclude_from_ban_count"`
}

type ContentModerationConfigView struct {
	AI                             ContentModerationAIConfigView      `json:"ai"`
	Enabled                        bool                               `json:"enabled"`
	Mode                           string                             `json:"mode"`
	BaseURL                        string                             `json:"base_url"`
	Model                          string                             `json:"model"`
	APIKeyConfigured               bool                               `json:"api_key_configured"`
	APIKeyMasked                   string                             `json:"api_key_masked"`
	APIKeyCount                    int                                `json:"api_key_count"`
	APIKeyMasks                    []string                           `json:"api_key_masks"`
	APIKeyStatuses                 []ContentModerationAPIKeyStatus    `json:"api_key_statuses"`
	TimeoutMS                      int                                `json:"timeout_ms"`
	SampleRate                     int                                `json:"sample_rate"`
	AllGroups                      bool                               `json:"all_groups"`
	GroupIDs                       []int64                            `json:"group_ids"`
	RecordNonHits                  bool                               `json:"record_non_hits"`
	Thresholds                     map[string]float64                 `json:"thresholds"`
	WorkerCount                    int                                `json:"worker_count"`
	QueueSize                      int                                `json:"queue_size"`
	BlockStatus                    int                                `json:"block_status"`
	BlockMessage                   string                             `json:"block_message"`
	EmailOnHit                     bool                               `json:"email_on_hit"`
	AutoBanEnabled                 bool                               `json:"auto_ban_enabled"`
	BanThreshold                   int                                `json:"ban_threshold"`
	DeductBalanceOnBlockEnabled    bool                               `json:"deduct_balance_on_block_enabled"`
	DeductBalanceOnBlockAmount     float64                            `json:"deduct_balance_on_block_amount"`
	PermanentBanEnabled            bool                               `json:"permanent_ban_enabled"`
	PermanentBanThreshold          int                                `json:"permanent_ban_threshold"`
	ViolationWindowHours           int                                `json:"violation_window_hours"`
	RetryCount                     int                                `json:"retry_count"`
	HitRetentionDays               int                                `json:"hit_retention_days"`
	NonHitRetentionDays            int                                `json:"non_hit_retention_days"`
	PreHashCheckEnabled            bool                               `json:"pre_hash_check_enabled"`
	BlockedKeywords                []string                           `json:"blocked_keywords"`
	KeywordBlockingMode            string                             `json:"keyword_blocking_mode"`
	KeywordRulePacks               []ContentModerationKeywordRulePack `json:"keyword_rule_packs"`
	ModelFilter                    ContentModerationModelFilter       `json:"model_filter"`
	PolicyVersion                  int                                `json:"policy_version"`
	LocalKeywordOnly               bool                               `json:"local_keyword_only"`
	AllowedLanguages               []string                           `json:"allowed_languages"`
	ScanLatestUserInput            bool                               `json:"scan_latest_user_input"`
	ScanNewSkillContent            bool                               `json:"scan_new_skill_content"`
	ScanNewToolOutput              bool                               `json:"scan_new_tool_output"`
	ScanTopLevelInstructions       bool                               `json:"scan_top_level_instructions"`
	KeywordGroupRoutingEnabled     bool                               `json:"keyword_group_routing_enabled"`
	CyberPolicyExcludeFromBanCount bool                               `json:"cyber_policy_exclude_from_ban_count"`
}

type ContentModerationKeywordRulePack struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Action      string   `json:"action"`
	Enabled     bool     `json:"enabled"`
	Keywords    []string `json:"keywords"`
	BuiltIn     bool     `json:"built_in"`
}

// ContentModerationKeywordRulePackOverride only records fields that an
// administrator explicitly changed. Keeping this as a sparse delta prevents a
// previously saved copy of the built-in defaults from silently overriding a
// newer public catalog action or enabled state.
type ContentModerationKeywordRulePackOverride struct {
	ID      string  `json:"id"`
	Enabled *bool   `json:"enabled,omitempty"`
	Action  *string `json:"action,omitempty"`
}

type ContentModerationAPIKeyStatus struct {
	Index          int        `json:"index"`
	KeyHash        string     `json:"key_hash"`
	Masked         string     `json:"masked"`
	Status         string     `json:"status"`
	FailureCount   int        `json:"failure_count"`
	SuccessCount   int64      `json:"success_count"`
	LastError      string     `json:"last_error"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	FrozenUntil    *time.Time `json:"frozen_until,omitempty"`
	LastLatencyMS  int        `json:"last_latency_ms"`
	LastHTTPStatus int        `json:"last_http_status"`
	LastTested     bool       `json:"last_tested"`
	Configured     bool       `json:"configured"`
}

type ContentModerationAPIKeyLoad struct {
	Index          int    `json:"index"`
	KeyHash        string `json:"key_hash"`
	Masked         string `json:"masked"`
	Status         string `json:"status"`
	Active         int64  `json:"active"`
	Total          int64  `json:"total"`
	Success        int64  `json:"success"`
	Errors         int64  `json:"errors"`
	AvgLatencyMS   int64  `json:"avg_latency_ms"`
	LastLatencyMS  int    `json:"last_latency_ms"`
	LastHTTPStatus int    `json:"last_http_status"`
}

type TestContentModerationAPIKeysInput struct {
	APIKeys   []string `json:"api_keys"`
	BaseURL   string   `json:"base_url"`
	Model     string   `json:"model"`
	TimeoutMS int      `json:"timeout_ms"`
	Prompt    string   `json:"prompt"`
	Images    []string `json:"images"`
}

type TestContentModerationAPIKeysResult struct {
	Items       []ContentModerationAPIKeyStatus   `json:"items"`
	AuditResult *ContentModerationTestAuditResult `json:"audit_result,omitempty"`
	ImageCount  int                               `json:"image_count"`
}

type ContentModerationTestAuditResult struct {
	Flagged         bool               `json:"flagged"`
	HighestCategory string             `json:"highest_category"`
	HighestScore    float64            `json:"highest_score"`
	CompositeScore  float64            `json:"composite_score"`
	CategoryScores  map[string]float64 `json:"category_scores"`
	Thresholds      map[string]float64 `json:"thresholds"`
}

type UpdateContentModerationConfigInput struct {
	AI                             *UpdateContentModerationAIConfigInput `json:"ai"`
	Enabled                        *bool                                 `json:"enabled"`
	Mode                           *string                               `json:"mode"`
	BaseURL                        *string                               `json:"base_url"`
	Model                          *string                               `json:"model"`
	APIKey                         *string                               `json:"api_key"`
	APIKeys                        *[]string                             `json:"api_keys"`
	APIKeysMode                    string                                `json:"api_keys_mode"`
	DeleteAPIKeyHashes             *[]string                             `json:"delete_api_key_hashes"`
	ClearAPIKey                    bool                                  `json:"clear_api_key"`
	TimeoutMS                      *int                                  `json:"timeout_ms"`
	SampleRate                     *int                                  `json:"sample_rate"`
	AllGroups                      *bool                                 `json:"all_groups"`
	GroupIDs                       *[]int64                              `json:"group_ids"`
	RecordNonHits                  *bool                                 `json:"record_non_hits"`
	Thresholds                     *map[string]float64                   `json:"thresholds"`
	WorkerCount                    *int                                  `json:"worker_count"`
	QueueSize                      *int                                  `json:"queue_size"`
	BlockStatus                    *int                                  `json:"block_status"`
	BlockMessage                   *string                               `json:"block_message"`
	EmailOnHit                     *bool                                 `json:"email_on_hit"`
	AutoBanEnabled                 *bool                                 `json:"auto_ban_enabled"`
	BanThreshold                   *int                                  `json:"ban_threshold"`
	DeductBalanceOnBlockEnabled    *bool                                 `json:"deduct_balance_on_block_enabled"`
	DeductBalanceOnBlockAmount     *float64                              `json:"deduct_balance_on_block_amount"`
	PermanentBanEnabled            *bool                                 `json:"permanent_ban_enabled"`
	PermanentBanThreshold          *int                                  `json:"permanent_ban_threshold"`
	ViolationWindowHours           *int                                  `json:"violation_window_hours"`
	RetryCount                     *int                                  `json:"retry_count"`
	HitRetentionDays               *int                                  `json:"hit_retention_days"`
	NonHitRetentionDays            *int                                  `json:"non_hit_retention_days"`
	PreHashCheckEnabled            *bool                                 `json:"pre_hash_check_enabled"`
	BlockedKeywords                *[]string                             `json:"blocked_keywords"`
	KeywordBlockingMode            *string                               `json:"keyword_blocking_mode"`
	KeywordRulePacks               *[]ContentModerationKeywordRulePack   `json:"keyword_rule_packs"`
	ModelFilter                    *ContentModerationModelFilter         `json:"model_filter"`
	AllowedLanguages               *[]string                             `json:"allowed_languages"`
	ScanLatestUserInput            *bool                                 `json:"scan_latest_user_input"`
	ScanNewSkillContent            *bool                                 `json:"scan_new_skill_content"`
	ScanNewToolOutput              *bool                                 `json:"scan_new_tool_output"`
	ScanTopLevelInstructions       *bool                                 `json:"scan_top_level_instructions"`
	KeywordGroupRoutingEnabled     *bool                                 `json:"keyword_group_routing_enabled"`
	CyberPolicyExcludeFromBanCount *bool                                 `json:"cyber_policy_exclude_from_ban_count"`
}

type ContentModerationModelFilter struct {
	Type   string   `json:"type"`
	Models []string `json:"models"`
}

type ContentModerationCheckInput struct {
	// AdmissionID and Turn are generated by the gateway, never client headers.
	AdmissionID        string
	Turn               uint64
	GatewayNodeID      string
	Region             string
	RequestID          string
	UserID             int64
	UserEmail          string
	OrganizationID     int64
	BillingSubjectType string
	BillingSubjectID   int64
	ControlPlaneKeyID  string
	APIKeyID           int64
	// IsolationScopeID 是仅用于进程内被拒轮次指纹的稳定 Key 命名空间。
	// 主站快照 Key 没有本地数据库 ID，因此它与审计日志 APIKeyID 分开。
	IsolationScopeID   int64
	IsolationSessionID string
	APIKeyName         string
	GroupID            *int64
	GroupName          string
	Endpoint           string
	Provider           string
	Model              string
	Protocol           string
	Body               []byte
	// ResponsesWebSocket is set only by the server's response.create ingress.
	// HTTP bodies/headers cannot opt into the WebSocket envelope exception.
	ResponsesWebSocket                 bool
	OriginalGroupAllowsBlockedKeywords bool
	KeywordRouteResolver               KeywordRouteResolver
	KeywordRouteRequiresReconnect      bool
	BoundAccountAllowsBlockedKeywords  *bool
	// LocalPolicyOnly is used by voice protocols whose text is already inside
	// this gateway. It permits language and keyword rules but never calls a
	// third-party moderation API, regardless of the administrator's global mode.
	LocalPolicyOnly bool
	// DisableHistoryIsolation keeps event-scoped protocols stateless: a blocked
	// Realtime frame must neither inherit nor create blocked-turn fingerprints.
	DisableHistoryIsolation bool
}

type KeywordRouteResolver func(ctx context.Context, model, protocol string, body []byte, allowCrossGroup bool) (*KeywordRouteResult, error)

type KeywordRouteResult struct {
	Group              *Group
	Subscription       *UserSubscription
	ExcludedAccountIDs map[int64]struct{}
}

type ContentModerationInput struct {
	Text     string
	Images   []string
	Segments []ContentModerationSegment
}

type ContentModerationSegment struct {
	Source            string `json:"source"`
	Protocol          string `json:"protocol"`
	CallID            string `json:"call_id,omitempty"`
	InstructionOrigin string `json:"instruction_origin,omitempty"`
	Text              string `json:"text"`
}

func (in *ContentModerationInput) Normalize() {
	if in == nil {
		return
	}
	// Text 仅保留 256K 作为 hash（指纹）和兼容审查摘要；本地关键词会从
	// Segments 为普通内容和 instructions 分配独立预算。外部审查兼容接口
	// 仍在 ModerationInput 中单独限制为 12K，不改变历史请求边界。
	in.Text = trimRunes(normalizeContentModerationText(in.Text), maxContentPolicyTextRunes)
	in.Images = normalizeModerationImages(in.Images)
}

func (in ContentModerationInput) IsEmpty() bool {
	return strings.TrimSpace(in.Text) == "" && len(in.Images) == 0
}

func (in ContentModerationInput) ModerationInput() any {
	images := limitContentModerationImages(in.Images)
	moderationText := trimRunes(in.Text, maxModerationInputRunes)
	if len(images) == 0 {
		return moderationText
	}
	parts := make([]moderationAPIInputPart, 0, len(images)+1)
	if strings.TrimSpace(moderationText) != "" {
		parts = append(parts, moderationAPIInputPart{Type: "text", Text: moderationText})
	}
	for _, image := range images {
		parts = append(parts, moderationAPIInputPart{
			Type:     "image_url",
			ImageURL: &moderationAPIImageURLRef{URL: image},
		})
	}
	return parts
}

func (in ContentModerationInput) ExcerptText() string {
	return in.Text
}

func (in ContentModerationInput) Hash() string {
	h := sha256.New()
	_, _ = h.Write([]byte("text:"))
	_, _ = h.Write([]byte(in.Text))
	for _, image := range in.Images {
		imageHash := sha256.Sum256([]byte(image))
		_, _ = h.Write([]byte("\nimage:"))
		_, _ = h.Write([]byte(hex.EncodeToString(imageHash[:])))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type ContentModerationDecision struct {
	ConfigFingerprint     string                         `json:"config_fingerprint,omitempty"`
	Test                  *ContentModerationAITestStatus `json:"test,omitempty"`
	ProviderHTTPStatus    int                            `json:"provider_http_status,omitempty"`
	Verdict               string                         `json:"verdict,omitempty"`
	ErrorCode             string                         `json:"error_code,omitempty"`
	Coverage              ContentModerationAICoverage    `json:"coverage"`
	Categories            []string                       `json:"categories,omitempty"`
	Reason                string                         `json:"reason,omitempty"`
	ProviderCalled        bool                           `json:"provider_called"`
	Phase                 string                         `json:"phase,omitempty"`
	ProviderLatencyMS     int64                          `json:"provider_latency_ms"`
	LatencyMS             int64                          `json:"latency_ms"`
	Shadow                bool                           `json:"shadow"`
	PenaltyState          string                         `json:"penalty_state"`
	Allowed               bool                           `json:"allowed"`
	Blocked               bool                           `json:"blocked"`
	Flagged               bool                           `json:"flagged"`
	Message               string                         `json:"message"`
	StatusCode            int                            `json:"status_code"`
	InputHash             string                         `json:"input_hash,omitempty"`
	HighestCategory       string                         `json:"highest_category"`
	HighestScore          float64                        `json:"highest_score"`
	CategoryScores        map[string]float64             `json:"category_scores"`
	Action                string                         `json:"action"`
	MatchedKeyword        string                         `json:"matched_keyword,omitempty"`
	ContentSource         string                         `json:"content_source,omitempty"`
	DetectedLanguages     []string                       `json:"detected_languages,omitempty"`
	OriginalGroupID       int64                          `json:"original_group_id,omitempty"`
	EffectiveGroupID      int64                          `json:"effective_group_id,omitempty"`
	Rerouted              bool                           `json:"rerouted"`
	RouteReason           string                         `json:"route_reason,omitempty"`
	EffectiveGroup        *Group                         `json:"-"`
	EffectiveSubscription *UserSubscription              `json:"-"`
	ExcludedAccountIDs    map[int64]struct{}             `json:"-"`
}

// ContentModerationLanguageEvidence 只保存可复核的统计，不保存命中的字符、
// 词语或客户原文。basis point（万分比）使用整数，避免数据库与前端浮点舍入
// 后对同一条请求给出不同结论。
type ContentModerationLanguageEvidence struct {
	DetectorVersion     string         `json:"detector_version"`
	ContentRunes        int            `json:"content_runes"`
	SampledRunes        int            `json:"sampled_runes"`
	TotalLetterRunes    int            `json:"total_letter_runes"`
	ScriptCounts        map[string]int `json:"script_counts,omitempty"`
	LanguageCounts      map[string]int `json:"language_counts,omitempty"`
	LanguageRatioBPS    map[string]int `json:"language_ratio_bps,omitempty"`
	LatinEvidenceCounts map[string]int `json:"latin_evidence_counts,omitempty"`
	MinLanguageRunes    int            `json:"min_language_runes"`
	MinLanguageRatioBPS int            `json:"min_language_ratio_bps"`
}

type ContentModerationLog struct {
	ID                    int64                             `json:"id"`
	RequestID             string                            `json:"request_id"`
	UserID                *int64                            `json:"user_id,omitempty"`
	UserEmail             string                            `json:"user_email"`
	OrganizationID        int64                             `json:"organization_id,omitempty"`
	BillingSubjectType    string                            `json:"billing_subject_type,omitempty"`
	BillingSubjectID      int64                             `json:"billing_subject_id,omitempty"`
	ControlPlaneKeyID     string                            `json:"control_plane_key_id,omitempty"`
	IsolationScopeID      int64                             `json:"-"`
	APIKeyID              *int64                            `json:"api_key_id,omitempty"`
	APIKeyName            string                            `json:"api_key_name"`
	GroupID               *int64                            `json:"group_id,omitempty"`
	GroupName             string                            `json:"group_name"`
	OriginalGroupID       *int64                            `json:"original_group_id,omitempty"`
	OriginalGroupName     string                            `json:"original_group_name"`
	EffectiveGroupID      *int64                            `json:"effective_group_id,omitempty"`
	EffectiveGroupName    string                            `json:"effective_group_name"`
	ContentSource         string                            `json:"content_source,omitempty"`
	DetectedLanguages     []string                          `json:"detected_languages,omitempty"`
	ContentFingerprint    string                            `json:"content_fingerprint,omitempty"`
	LanguageEvidence      ContentModerationLanguageEvidence `json:"language_evidence"`
	KeywordPolicyRerouted bool                              `json:"keyword_policy_rerouted"`
	KeywordPolicyReason   string                            `json:"keyword_policy_reason,omitempty"`
	Endpoint              string                            `json:"endpoint"`
	Provider              string                            `json:"provider"`
	Model                 string                            `json:"model"`
	Mode                  string                            `json:"mode"`
	Action                string                            `json:"action"`
	Flagged               bool                              `json:"flagged"`
	MatchedKeyword        string                            `json:"matched_keyword,omitempty"`
	HighestCategory       string                            `json:"highest_category"`
	HighestScore          float64                           `json:"highest_score"`
	CategoryScores        map[string]float64                `json:"category_scores"`
	ThresholdSnapshot     map[string]float64                `json:"threshold_snapshot"`
	InputExcerpt          string                            `json:"input_excerpt"`
	// 全文仅管理员详情接口读取；列表使用 InputExcerpt，避免分页重复搬运大段客户内容。
	FullInputText  string         `json:"full_input_text,omitempty"`
	AIReviewDetail map[string]any `json:"ai_review_detail,omitempty"`
	// 保留字段名；现在保存完整送审文本，不截断。图片仅在详情中记录元信息。
	ReviewTextExcerpt string    `json:"review_text_excerpt,omitempty"`
	UpstreamLatencyMS *int      `json:"upstream_latency_ms,omitempty"`
	Error             string    `json:"error"`
	ViolationCount    int       `json:"violation_count"`
	AutoBanned        bool      `json:"auto_banned"`
	EmailSent         bool      `json:"email_sent"`
	UserStatus        string    `json:"user_status"`
	QueueDelayMS      *int      `json:"queue_delay_ms,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type ContentModerationLogFilter struct {
	Pagination       pagination.PaginationParams
	Result           string
	GroupID          *int64
	OriginalGroupID  *int64
	EffectiveGroupID *int64
	ContentSource    string
	DetectedLanguage string
	RouteReason      string
	Endpoint         string
	Search           string
	From             *time.Time
	To               *time.Time
}

type ContentModerationCleanupResult struct {
	DeletedHit    int64     `json:"deleted_hit"`
	DeletedNonHit int64     `json:"deleted_non_hit"`
	FinishedAt    time.Time `json:"finished_at"`
}

type ContentModerationRuntimeStatus struct {
	AI                           *ContentModerationAIMetrics     `json:"ai,omitempty"`
	Enabled                      bool                            `json:"enabled"`
	RiskControlEnabled           bool                            `json:"risk_control_enabled"`
	Mode                         string                          `json:"mode"`
	WorkerCount                  int                             `json:"worker_count"`
	MaxWorkers                   int                             `json:"max_workers"`
	ActiveWorkers                int                             `json:"active_workers"`
	IdleWorkers                  int                             `json:"idle_workers"`
	QueueSize                    int                             `json:"queue_size"`
	QueueLength                  int                             `json:"queue_length"`
	QueueUsagePercent            float64                         `json:"queue_usage_percent"`
	Enqueued                     int64                           `json:"enqueued"`
	Dropped                      int64                           `json:"dropped"`
	Processed                    int64                           `json:"processed"`
	Errors                       int64                           `json:"errors"`
	PreBlockActive               int                             `json:"pre_block_active"`
	PreBlockChecked              int64                           `json:"pre_block_checked"`
	PreBlockAllowed              int64                           `json:"pre_block_allowed"`
	PreBlockBlocked              int64                           `json:"pre_block_blocked"`
	PreBlockErrors               int64                           `json:"pre_block_errors"`
	PreBlockAvgLatencyMS         int64                           `json:"pre_block_avg_latency_ms"`
	PreBlockAPIKeyActive         int64                           `json:"pre_block_api_key_active"`
	PreBlockAPIKeyAvailableCount int64                           `json:"pre_block_api_key_available_count"`
	PreBlockAPIKeyTotalCalls     int64                           `json:"pre_block_api_key_total_calls"`
	PreBlockAPIKeyLoads          []ContentModerationAPIKeyLoad   `json:"pre_block_api_key_loads"`
	APIKeyStatuses               []ContentModerationAPIKeyStatus `json:"api_key_statuses"`
	FlaggedHashCount             int64                           `json:"flagged_hash_count"`
	LastCleanupAt                *time.Time                      `json:"last_cleanup_at,omitempty"`
	LastCleanupDeletedHit        int64                           `json:"last_cleanup_deleted_hit"`
	LastCleanupDeletedNonHit     int64                           `json:"last_cleanup_deleted_non_hit"`
}

type ContentModerationUnbanUserResult struct {
	UserID int64  `json:"user_id"`
	Status string `json:"status"`
}

type ContentModerationDeleteHashResult struct {
	InputHash string `json:"input_hash"`
	Deleted   bool   `json:"deleted"`
}

type ContentModerationClearHashesResult struct {
	Deleted int64 `json:"deleted"`
}

type ContentModerationUserPenalty struct {
	UserID              int64      `json:"user_id"`
	BlockedCount        int        `json:"blocked_count"`
	DeductedBalance     float64    `json:"deducted_balance"`
	PermanentlyBanned   bool       `json:"permanently_banned"`
	PermanentBanReason  string     `json:"permanent_ban_reason"`
	LastBlockedAt       *time.Time `json:"last_blocked_at,omitempty"`
	PermanentlyBannedAt *time.Time `json:"permanently_banned_at,omitempty"`
}

type ContentModerationRecordUserPenaltyInput struct {
	UserID                int64
	DeductBalance         float64
	PermanentBanEnabled   bool
	PermanentBanThreshold int
	PermanentBanReason    string
}

type ContentModerationRepository interface {
	CreateLog(ctx context.Context, log *ContentModerationLog) error
	ListLogs(ctx context.Context, filter ContentModerationLogFilter) ([]ContentModerationLog, *pagination.PaginationResult, error)
	CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time, excludeCyberPolicy bool) (int, error)
	RecordUserPenalty(ctx context.Context, input ContentModerationRecordUserPenaltyInput) (*ContentModerationUserPenalty, error)
	IsUserPermanentlyBanned(ctx context.Context, userID int64) (bool, error)
	CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*ContentModerationCleanupResult, error)
	// UpdateLogEmailSent 回写邮件发送结果（F7：CreateLog 先行后补 EmailSent）。
	UpdateLogEmailSent(ctx context.Context, id int64, sent bool) error
}

type ContentModerationPenaltyScope struct {
	UserID            int64
	OrganizationID    int64
	ControlPlaneKeyID string
}

type contentModerationSubjectCounter interface {
	CountFlaggedBySubjectSince(ctx context.Context, scope ContentModerationPenaltyScope, since time.Time, excludeCyberPolicy bool) (int, error)
}

type ContentModerationHashCache interface {
	RecordFlaggedInputHash(ctx context.Context, inputHash string) error
	HasFlaggedInputHash(ctx context.Context, inputHash string) (bool, error)
	DeleteFlaggedInputHash(ctx context.Context, inputHash string) (bool, error)
	ClearFlaggedInputHashes(ctx context.Context) (int64, error)
	CountFlaggedInputHashes(ctx context.Context) (int64, error)
}

type ContentModerationService struct {
	aiOnce                   sync.Once
	aiState                  *contentModerationAIRuntime
	settingRepo              SettingRepository
	repo                     ContentModerationRepository
	hashCache                ContentModerationHashCache
	groupRepo                GroupRepository
	userRepo                 UserRepository
	authCacheInvalidator     APIKeyAuthCacheInvalidator
	emailService             *EmailService
	httpClient               *http.Client
	asyncQueue               chan contentModerationTask
	workerCount              int
	workerSpawnMu            sync.Mutex
	apiKeyCursor             atomic.Uint64
	asyncActive              atomic.Int64
	asyncEnqueued            atomic.Int64
	asyncDropped             atomic.Int64
	asyncProcessed           atomic.Int64
	asyncErrors              atomic.Int64
	preBlockActive           atomic.Int64
	preBlockChecked          atomic.Int64
	preBlockAllowed          atomic.Int64
	preBlockBlocked          atomic.Int64
	preBlockErrors           atomic.Int64
	preBlockLatencyTotalMS   atomic.Int64
	lastCleanupUnix          atomic.Int64
	lastCleanupDeletedHit    atomic.Int64
	lastCleanupDeletedNonHit atomic.Int64
	runtimeSnapshot          atomic.Pointer[contentModerationRuntimeSnapshot]
	runtimeRefreshMu         sync.Mutex
	runtimeCacheTTL          time.Duration
	runtimeRefreshRetryAt    atomic.Int64
	keywordCatalog           *KeywordCatalogService
	blockedTurnIsolationOnce sync.Once
	blockedTurnIsolation     *blockedTurnFingerprintCache
	keyHealthMu              sync.Mutex
	keyHealth                map[string]*contentModerationKeyHealth
}

type contentModerationRuntimeSnapshot struct {
	riskControlEnabled bool
	config             *ContentModerationConfig
	keywordRuleMatcher *contentModerationKeywordRuleMatcher
	// allowlistedUsers 是 cyber 只记录不拦截的用户 ID 白名单（官方 v0.2.13 语义）：
	// 白名单用户命中 cyber_policy 时照常留证，但不参与自动封号判定。
	allowlistedUsers map[int64]struct{}
	configDigest     [sha256.Size]byte
	catalogDigest    string
	loadedAt         time.Time
}

// isUserAllowlisted 判断该用户是否在 cyber 白名单里（只记录不拦截）。
// nil 快照 / 非法 ID / 白名单未加载一律按「不在白名单」处理（fail-closed：
// 宁可多拦一次，也不因为读不到配置就静默放行）。
func (s *contentModerationRuntimeSnapshot) isUserAllowlisted(userID int64) bool {
	if s == nil || userID <= 0 || len(s.allowlistedUsers) == 0 {
		return false
	}
	_, ok := s.allowlistedUsers[userID]
	return ok
}

type contentModerationTask struct {
	input            ContentModerationCheckInput
	content          ContentModerationInput
	inputHash        string
	log              *ContentModerationLog
	config           *ContentModerationConfig
	recordHash       bool
	applySideEffects bool
	enqueuedAt       time.Time
}

type contentModerationKeyHealth struct {
	Hash           string
	Masked         string
	FailureCount   int
	SuccessCount   int64
	LastError      string
	LastCheckedAt  time.Time
	FrozenUntil    time.Time
	LastLatencyMS  int
	LastHTTPStatus int
	LastTested     bool
	SyncActive     int64
	SyncTotal      int64
	SyncSuccess    int64
	SyncErrors     int64
	SyncLatencyMS  int64
}

func NewContentModerationService(
	settingRepo SettingRepository,
	repo ContentModerationRepository,
	hashCache ContentModerationHashCache,
	groupRepo GroupRepository,
	userRepo UserRepository,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	emailService *EmailService,
) *ContentModerationService {
	svc := &ContentModerationService{
		settingRepo:          settingRepo,
		repo:                 repo,
		hashCache:            hashCache,
		groupRepo:            groupRepo,
		userRepo:             userRepo,
		authCacheInvalidator: authCacheInvalidator,
		emailService:         emailService,
		httpClient:           &http.Client{},
		// workerCount 由 ensureWorkers 维护（0 → 已启动数），不再在构造处按上限预置 32。
		// 队列缓冲按“默认队列深度”分配，而不是按 maxContentModerationQueueSize（100000）。
		//
		// 入队准入用的是运行时设置 cfg.QueueSize（默认 defaultContentModerationQueueSize=32768，
		// 超过 maxContentModerationQueueSize 会被夹到 100000），并且满了就丢弃并计数
		// (asyncDropped) —— 也就是说当 QueueSize 为默认值时，缓冲里**永远不可能**超过 32768 个元素，
		// 按 100000 分配出来的那 67232 个槽位是不可达的。
		//
		// 单个 contentModerationTask 约 416 字节，100000 个槽位 ≈ 40MB，且这部分内存在构造时
		// 无条件预分配、与内容审核是否启用无关（生产实测：enabled=false 仍占用）。
		// 改成按默认深度分配后，默认配置下行为完全等价，RSS 约省 28MB。
		// 若运维显式把 QueueSize 调到 32768 以上，准入仍按 QueueSize 判定，超出缓冲的部分
		// 走既有的 select default 分支丢弃并告警（语义不变，只是丢弃点提前）。
		asyncQueue: make(chan contentModerationTask, defaultContentModerationQueueSize),
		keyHealth:  make(map[string]*contentModerationKeyHealth),
	}
	if settingRepo != nil && repo != nil {
		// 只按默认值起 worker，避免为「运行时可能调大」预留 28 个空转 goroutine；
		// 实际工作量由 worker() 的 id 门控（id >= cfg.WorkerCount）决定，
		// worker_count 被调大时由 workerSupervisor 周期补齐。
		svc.ensureWorkers(defaultContentModerationWorkerCount)
		go svc.cleanupWorker()
		go svc.workerSupervisor()
	}
	return svc
}

// SetKeywordCatalog injects an atomically refreshed official base catalog.
// Request checks only read its immutable snapshot and never perform remote IO.
func (s *ContentModerationService) SetKeywordCatalog(catalog *KeywordCatalogService) {
	if s == nil {
		return
	}
	s.runtimeRefreshMu.Lock()
	s.keywordCatalog = catalog
	s.runtimeSnapshot.Store(nil)
	s.runtimeRefreshRetryAt.Store(0)
	s.runtimeRefreshMu.Unlock()
}

func (s *ContentModerationService) KeywordCatalogStatus() KeywordCatalogStatus {
	if s == nil || s.keywordCatalog == nil {
		return KeywordCatalogStatus{}
	}
	return s.keywordCatalog.Status()
}

func (s *ContentModerationService) ForceKeywordCatalogUpdate(ctx context.Context) (KeywordCatalogStatus, error) {
	if s == nil || s.keywordCatalog == nil {
		return KeywordCatalogStatus{}, errors.New("keyword catalog service unavailable")
	}
	status, err := s.keywordCatalog.ForceUpdate(ctx)
	if err != nil {
		return status, err
	}
	// Keep the old compiled matcher serving requests while the new catalog is
	// compiled in the background. Clearing it here would force the next request
	// to synchronously read settings and briefly fail open when that read fails.
	s.triggerRuntimeSnapshotRefresh()
	return status, nil
}

func (s *ContentModerationService) GetConfig(ctx context.Context) (*ContentModerationConfigView, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return s.configView(cfg), nil
}

func (s *ContentModerationService) UpdateConfig(ctx context.Context, input UpdateContentModerationConfigInput) (*ContentModerationConfigView, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if input.Enabled != nil {
		cfg.Enabled = *input.Enabled
	}
	if input.Mode != nil {
		cfg.Mode = strings.TrimSpace(*input.Mode)
	}
	if input.BaseURL != nil {
		cfg.BaseURL = strings.TrimSpace(*input.BaseURL)
	}
	if input.Model != nil {
		cfg.Model = strings.TrimSpace(*input.Model)
	}
	if input.TimeoutMS != nil {
		cfg.TimeoutMS = *input.TimeoutMS
	}
	if input.SampleRate != nil {
		cfg.SampleRate = *input.SampleRate
	}
	if input.WorkerCount != nil {
		cfg.WorkerCount = *input.WorkerCount
	}
	if input.QueueSize != nil {
		cfg.QueueSize = *input.QueueSize
	}
	if input.BlockStatus != nil {
		cfg.BlockStatus = *input.BlockStatus
	}
	if input.BlockMessage != nil {
		cfg.BlockMessage = strings.TrimSpace(*input.BlockMessage)
	}
	if input.EmailOnHit != nil {
		cfg.EmailOnHit = *input.EmailOnHit
	}
	if input.AutoBanEnabled != nil {
		cfg.AutoBanEnabled = *input.AutoBanEnabled
	}
	if input.BanThreshold != nil {
		cfg.BanThreshold = *input.BanThreshold
	}
	if input.DeductBalanceOnBlockEnabled != nil {
		cfg.DeductBalanceOnBlockEnabled = *input.DeductBalanceOnBlockEnabled
	}
	if input.DeductBalanceOnBlockAmount != nil {
		cfg.DeductBalanceOnBlockAmount = *input.DeductBalanceOnBlockAmount
	}
	if input.PermanentBanEnabled != nil {
		cfg.PermanentBanEnabled = *input.PermanentBanEnabled
	}
	if input.PermanentBanThreshold != nil {
		cfg.PermanentBanThreshold = *input.PermanentBanThreshold
	}
	if input.ViolationWindowHours != nil {
		cfg.ViolationWindowHours = *input.ViolationWindowHours
	}
	if input.RetryCount != nil {
		cfg.RetryCount = *input.RetryCount
	}
	if input.HitRetentionDays != nil {
		cfg.HitRetentionDays = *input.HitRetentionDays
	}
	if input.NonHitRetentionDays != nil {
		cfg.NonHitRetentionDays = *input.NonHitRetentionDays
	}
	if input.PreHashCheckEnabled != nil {
		cfg.PreHashCheckEnabled = *input.PreHashCheckEnabled
	}
	if input.BlockedKeywords != nil {
		cfg.BlockedKeywords = normalizeBlockedKeywords(*input.BlockedKeywords)
	}
	if input.KeywordBlockingMode != nil {
		cfg.KeywordBlockingMode = strings.TrimSpace(*input.KeywordBlockingMode)
	}
	if input.KeywordRulePacks != nil {
		cfg.KeywordRulePacks = *input.KeywordRulePacks
		cfg.KeywordRulePackOverrides = buildContentModerationKeywordRulePackOverrides(
			s.keywordCatalogBasePacks(),
			*input.KeywordRulePacks,
		)
		cfg.KeywordCatalogOverrideVersion = 1
	}
	if input.ModelFilter != nil {
		cfg.ModelFilter = *input.ModelFilter
	}
	if input.AllowedLanguages != nil {
		cfg.AllowedLanguages = normalizeAllowedLanguages(*input.AllowedLanguages)
	}
	if input.ScanLatestUserInput != nil {
		cfg.ScanLatestUserInput = *input.ScanLatestUserInput
	}
	if input.ScanNewSkillContent != nil {
		cfg.ScanNewSkillContent = *input.ScanNewSkillContent
	}
	if input.ScanNewToolOutput != nil {
		cfg.ScanNewToolOutput = *input.ScanNewToolOutput
	}
	if input.ScanTopLevelInstructions != nil {
		cfg.ScanTopLevelInstructions = *input.ScanTopLevelInstructions
	}
	if input.KeywordGroupRoutingEnabled != nil {
		cfg.KeywordGroupRoutingEnabled = *input.KeywordGroupRoutingEnabled
	}
	if input.AllGroups != nil {
		cfg.AllGroups = *input.AllGroups
	}
	if input.GroupIDs != nil {
		cfg.GroupIDs = normalizeInt64IDs(*input.GroupIDs)
	}
	if input.RecordNonHits != nil {
		cfg.RecordNonHits = *input.RecordNonHits
	}
	if input.CyberPolicyExcludeFromBanCount != nil {
		cfg.CyberPolicyExcludeFromBanCount = *input.CyberPolicyExcludeFromBanCount
	}
	if input.Thresholds != nil {
		cfg.Thresholds = mergeContentModerationThresholds(ContentModerationDefaultThresholds(), *input.Thresholds)
	}
	if input.ClearAPIKey {
		cfg.APIKey = ""
		cfg.APIKeys = []string{}
	} else {
		apiKeysMode := normalizeContentModerationAPIKeysMode(input.APIKeysMode)
		if input.DeleteAPIKeyHashes != nil && apiKeysMode != contentModerationAPIKeysModeReplace {
			cfg.APIKeys = deleteModerationAPIKeysByHash(cfg.apiKeys(), *input.DeleteAPIKeyHashes)
			cfg.APIKey = ""
		}
		if input.APIKeys != nil {
			if apiKeysMode == contentModerationAPIKeysModeReplace {
				cfg.APIKeys = normalizeModerationAPIKeys(*input.APIKeys)
			} else {
				cfg.APIKeys = normalizeModerationAPIKeys(append(cfg.apiKeys(), *input.APIKeys...))
			}
			cfg.APIKey = ""
		}
		if input.APIKey != nil && strings.TrimSpace(*input.APIKey) != "" {
			cfg.APIKeys = normalizeModerationAPIKeys(append(cfg.APIKeys, *input.APIKey))
			cfg.APIKey = ""
		}
	}
	patchContentModerationAIConfig(&cfg.AI, input.AI)
	if input.KeywordBlockingMode != nil {
		switch cfg.KeywordBlockingMode {
		case ContentModerationKeywordModeKeywordOnly, ContentModerationKeywordModeAIOnly:
		default:
			return nil, infraerrors.BadRequest("INVALID_CONTENT_MODERATION_ENGINE", "请选择本地关键词或纯 AI 审查")
		}
	}
	if cfg.KeywordBlockingMode == ContentModerationKeywordModeAIOnly {
		// AI requires complete coverage; old sampling and keyword shortcuts must
		// not silently grant an unreviewed request permission to reach upstream.
		cfg.SampleRate = 100
		if input.Mode == nil && input.KeywordBlockingMode != nil {
			cfg.Mode = ContentModerationModeShadow
		}
		if input.AutoBanEnabled == nil {
			cfg.AutoBanEnabled = false
		}
		if input.DeductBalanceOnBlockEnabled == nil {
			cfg.DeductBalanceOnBlockEnabled = false
		}
		if input.PermanentBanEnabled == nil {
			cfg.PermanentBanEnabled = false
		}
		if input.EmailOnHit == nil {
			cfg.EmailOnHit = false
		}
	}
	cfg.PreHashCheckEnabled = false
	if err := s.validateConfig(ctx, cfg); err != nil {
		return nil, err
	}
	cfg.normalize()
	storedConfig := s.contentModerationConfigForStorage(cfg)
	raw, err := json.Marshal(storedConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal content moderation config: %w", err)
	}
	updates := map[string]string{SettingKeyContentModerationConfig: string(raw)}
	if storedConfig.Enabled {
		// 内容审计请求热路径还受风控总开关保护。启用内容审计时同步打开总开关，
		// 避免后台显示已开启但实际被外层总闸跳过。
		updates[SettingKeyRiskControlEnabled] = "true"
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return nil, fmt.Errorf("save content moderation config: %w", err)
	}
	s.replaceRuntimeConfig(storedConfig, raw)
	effectiveConfig := cloneContentModerationConfig(storedConfig)
	s.applyKeywordCatalog(effectiveConfig)
	return s.configView(effectiveConfig), nil
}

func (s *ContentModerationService) TestAPIKeys(ctx context.Context, input TestContentModerationAPIKeysInput) (*TestContentModerationAPIKeysResult, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	keys := normalizeModerationAPIKeys(input.APIKeys)
	configured := false
	if len(keys) == 0 {
		keys = cfg.apiKeys()
		configured = true
	}
	if strings.TrimSpace(input.BaseURL) != "" {
		cfg.BaseURL = input.BaseURL
	}
	if strings.TrimSpace(input.Model) != "" {
		cfg.Model = input.Model
	}
	if input.TimeoutMS > 0 {
		cfg.TimeoutMS = input.TimeoutMS
	}
	cfg.normalize()
	testInput, imageCount, err := buildModerationTestInput(input.Prompt, input.Images)
	if err != nil {
		return nil, err
	}
	auditOnly := contentModerationTestHasAuditInput(input.Prompt, input.Images)
	if configured && auditOnly {
		key, ok := s.nextUsableAPIKey(cfg)
		if !ok {
			return &TestContentModerationAPIKeysResult{
				Items:      s.apiKeyStatuses(keys),
				ImageCount: imageCount,
			}, nil
		}
		keys = []string{key}
	}
	if len(keys) == 0 {
		return &TestContentModerationAPIKeysResult{Items: []ContentModerationAPIKeyStatus{}, ImageCount: imageCount}, nil
	}
	items := make([]ContentModerationAPIKeyStatus, 0, len(keys))
	var auditResult *ContentModerationTestAuditResult
	for idx, key := range keys {
		start := time.Now()
		httpStatus := 0
		result, err := s.callModerationOnceWithInput(ctx, cfg, key, testInput, &httpStatus)
		latency := int(time.Since(start).Milliseconds())
		keyHash := moderationAPIKeyHash(key)
		if err != nil {
			s.markAPIKeyError(key, err.Error(), latency, httpStatus)
		} else {
			s.markAPIKeySuccess(key, latency, httpStatus)
			if auditResult == nil {
				auditResult = buildContentModerationTestAuditResult(result, cfg.Thresholds)
			}
		}
		status := s.apiKeyStatusForHash(idx, keyHash, maskSecretTail(key), configured)
		status.LastTested = true
		items = append(items, status)
	}
	return &TestContentModerationAPIKeysResult{Items: items, AuditResult: auditResult, ImageCount: imageCount}, nil
}

func (s *ContentModerationService) Check(ctx context.Context, input ContentModerationCheckInput) (*ContentModerationDecision, error) {
	allow := &ContentModerationDecision{Allowed: true, Action: ContentModerationActionAllow}
	if s == nil || s.settingRepo == nil || s.repo == nil {
		slog.Info("content_moderation.skip_unavailable",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503)), nil
	}
	runtimeSnapshot, err := s.loadRuntimeSnapshot(ctx)
	if err != nil {
		slog.Warn("content_moderation.skip_runtime_snapshot_load_failed",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"error", err)
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503)), nil
	}
	if runtimeSnapshot == nil || runtimeSnapshot.config == nil {
		slog.Warn("content_moderation.skip_runtime_snapshot_empty",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return contentModerationAIFailure(aiError("ai_config_unavailable", 503)), nil
	}
	if !runtimeSnapshot.riskControlEnabled {
		slog.Info("content_moderation.skip_feature_disabled",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return allow, nil
	}
	cfg := runtimeSnapshot.config
	inGroupScope := cfg.includesGroup(input.GroupID)
	inModelScope := cfg.includesModel(input.Model)
	slog.Debug("content_moderation.config_loaded",
		"user_id", input.UserID,
		"api_key_id", input.APIKeyID,
		"group_id", contentModerationLogGroupID(input.GroupID),
		"group_name", input.GroupName,
		"endpoint", input.Endpoint,
		"provider", input.Provider,
		"protocol", input.Protocol,
		"model", input.Model,
		"enabled", cfg.Enabled,
		"mode", cfg.Mode,
		"all_groups", cfg.AllGroups,
		"configured_group_ids", cfg.GroupIDs,
		"in_group_scope", inGroupScope,
		"model_filter_type", cfg.ModelFilter.Type,
		"configured_models", cfg.ModelFilter.Models,
		"in_model_scope", inModelScope,
		"sample_rate", cfg.SampleRate,
		"api_key_count", len(cfg.apiKeys()),
		"pre_hash_check_enabled", cfg.PreHashCheckEnabled,
		"record_non_hits", cfg.RecordNonHits)
	if !cfg.Enabled {
		slog.Info("content_moderation.skip_config_disabled",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return allow, nil
	}
	if cfg.Mode == ContentModerationModeOff {
		slog.Info("content_moderation.skip_mode_off",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return allow, nil
	}
	if !inGroupScope {
		slog.Info("content_moderation.skip_group_out_of_scope",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"group_name", input.GroupName,
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"all_groups", cfg.AllGroups,
			"configured_group_ids", cfg.GroupIDs)
		return allow, nil
	}
	if !inModelScope {
		slog.Info("content_moderation.skip_model_out_of_scope",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"group_name", input.GroupName,
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"model", input.Model,
			"model_filter_type", cfg.ModelFilter.Type,
			"configured_models", cfg.ModelFilter.Models)
		return allow, nil
	}
	if cfg.KeywordBlockingMode == ContentModerationKeywordModeAIOnly {
		return s.checkAI(ctx, input, cfg, false), nil
	}
	if _, err := strictJSON(input.Body); err != nil {
		return &ContentModerationDecision{Allowed: false, Blocked: true, Flagged: false, Action: ContentModerationActionError, StatusCode: http.StatusBadRequest, Message: "请求包含非法 JSON 或重复字段"}, nil
	}
	if cfg.ScanTopLevelInstructions {
		if statusCode, message := validateClientInstructions(input.Protocol, input.Body); statusCode != 0 {
			// 重复键会让不同 JSON 解析器选择不同值；超大值则会在 String() 和
			// NFKC 标准化时放大临时内存。两者都必须在提取原文前明确拒绝。
			slog.Warn("content_moderation.invalid_client_instructions",
				"user_id", input.UserID,
				"api_key_id", input.APIKeyID,
				"group_id", contentModerationLogGroupID(input.GroupID),
				"endpoint", input.Endpoint,
				"status_code", statusCode,
				"body_bytes", len(input.Body))
			return &ContentModerationDecision{
				Allowed: false, Blocked: true, Flagged: false,
				Message: message, StatusCode: statusCode, Action: ContentModerationActionError,
			}, nil
		}
	}
	segments, images := ExtractContentModerationSegments(input.Protocol, input.Body)
	content := ContentModerationInput{Segments: segments, Images: images}
	content = filterContentModerationInputForPolicy(content, cfg)
	if content.IsEmpty() {
		slog.Info("content_moderation.skip_empty_input",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"body_bytes", len(input.Body))
		return allow, nil
	}
	content.Normalize()
	// 各来源在审查副本中以不可匹配分隔符拼接，既共享一次语种/关键词扫描，
	// 又不会把两个无关 segment 的首尾误拼成一个关键词。
	policyText := contentModerationPolicyText(content)
	languageDetection := detectContentLanguagesDetailedNormalized(policyText)
	detectedLanguages := languageDetection.Languages
	if unsupported := unsupportedDetectedLanguages(detectedLanguages, cfg.AllowedLanguages); len(unsupported) > 0 {
		// 语种拦截发生在关键词匹配之前，过去这里没有关键词可反查来源，日志因而
		// 一直写空。提取器已经把本轮内容标成 user/skill/tool；直接汇总这些标签
		// 不需要再次扫描大文本，也不会保存客户原文。
		languageSource := contentModerationUnsupportedLanguageSource(content.Segments, unsupported)
		if cfg.Mode == ContentModerationModePreBlock {
			s.recordPreBlockSyncMetric(0, ContentModerationActionLanguageBlock)
		}
		highestCategory := "unsupported_language/" + strings.Join(unsupported, ",")
		scores := map[string]float64{highestCategory: 1}
		hashText := content.Hash()
		// 语种拦截不需要保存客户摘要；管理员通过来源、字符统计、比例和带随机
		// 盐的指纹复核即可。关键词日志仍沿用原有脱敏摘要，避免改变其他功能。
		log := s.buildLog(input, cfg, ContentModerationActionLanguageBlock, true, highestCategory, 1, scores, "", nil, nil, "")
		log.LanguageEvidence = languageDetection.Evidence
		applyContentPolicyLogEvidence(log, input, contentModerationLogGroupID(input.GroupID), input.GroupName, languageSource, detectedLanguages, false, "unsupported_language")
		// 语种限制只拒绝本次请求，不计违规次数、不扣余额、不封号。
		s.enqueueRecord(ctx, input, cfg, log, hashText, false, false)
		return &ContentModerationDecision{
			Allowed: false, Blocked: true, Flagged: true,
			Message:    "当前分组不支持本次请求使用的语种，请使用管理员允许的语种后重试",
			StatusCode: cfg.BlockStatus, HighestCategory: highestCategory, HighestScore: 1,
			CategoryScores: scores, Action: ContentModerationActionLanguageBlock,
			ContentSource:     languageSource,
			DetectedLanguages: detectedLanguages,
			OriginalGroupID:   contentModerationLogGroupID(input.GroupID),
			EffectiveGroupID:  contentModerationLogGroupID(input.GroupID),
			RouteReason:       "unsupported_language",
		}, nil
	}
	slog.Debug("content_moderation.input_extracted",
		"user_id", input.UserID,
		"api_key_id", input.APIKeyID,
		"group_id", contentModerationLogGroupID(input.GroupID),
		"endpoint", input.Endpoint,
		"protocol", input.Protocol,
		"text_runes", len([]rune(content.Text)),
		"image_count", len(content.Images))
	hashText := content.Hash()
	if cfg.Mode == ContentModerationModePreBlock {
		if cfg.KeywordBlockingMode != ContentModerationKeywordModeAPIOnly {
			keywordPolicyText := contentModerationKeywordPolicyText(content, policyText)
			if keywordMatch, hit := runtimeSnapshot.matchKeywordRulesNormalized(keywordPolicyText); hit {
				matchedSource := contentModerationMatchedSource(content.Segments, keywordMatch.Keyword)
				highestCategory := contentModerationKeywordCategory
				if keywordMatch.PackID != "" && keywordMatch.PackID != "custom" {
					highestCategory = contentModerationKeywordCategory + "/" + keywordMatch.PackID
				}
				scores := map[string]float64{highestCategory: 1.0}
				if keywordMatch.Action == ContentModerationActionKeywordAudit {
					slog.Info("content_moderation.keyword_audit",
						"user_id", input.UserID,
						"api_key_id", input.APIKeyID,
						"group_id", contentModerationLogGroupID(input.GroupID),
						"endpoint", input.Endpoint,
						"protocol", input.Protocol,
						"keyword_blocking_mode", cfg.KeywordBlockingMode,
						"rule_pack_id", keywordMatch.PackID,
						"rule_pack_name", keywordMatch.PackName,
						"keyword", keywordMatch.Keyword)
					log := s.buildLog(input, cfg, ContentModerationActionKeywordAudit, false, highestCategory, 1.0, scores, contentModerationMatchedExcerpt(content, keywordMatch.Keyword), nil, nil, "")
					log.MatchedKeyword = keywordMatch.Keyword
					applyContentPolicyLogEvidence(log, input, contentModerationLogGroupID(input.GroupID), input.GroupName, matchedSource, detectedLanguages, false, "audit_only")
					s.enqueueRecord(ctx, input, cfg, log, hashText, false, false)
					// audit-only（只审计）是规则包自身的最终动作，不能再交给允许分组
					// resolver（解析器）或旧版远程审查继续处理。否则同一个规则在不同
					// 历史配置下可能被切组甚至拒绝，违背后台“只审计”的明确语义。
					s.recordPreBlockSyncMetric(0, ContentModerationActionAllow)
					return allow, nil
				} else if input.KeywordRouteRequiresReconnect && input.BoundAccountAllowsBlockedKeywords != nil && *input.BoundAccountAllowsBlockedKeywords {
					log := s.buildLog(input, cfg, ContentModerationActionKeywordAllow, false, highestCategory, 1.0, scores, contentModerationMatchedExcerpt(content, keywordMatch.Keyword), nil, nil, "")
					log.MatchedKeyword = keywordMatch.Keyword
					applyContentPolicyLogEvidence(log, input, contentModerationLogGroupID(input.GroupID), input.GroupName, matchedSource, detectedLanguages, false, "allow_bound_account")
					s.enqueueRecord(ctx, input, cfg, log, hashText, false, false)
					return &ContentModerationDecision{
						Allowed: true, Flagged: true, Action: ContentModerationActionKeywordAllow,
						MatchedKeyword: keywordMatch.Keyword, ContentSource: matchedSource,
						DetectedLanguages: detectedLanguages,
						OriginalGroupID:   contentModerationLogGroupID(input.GroupID),
						EffectiveGroupID:  contentModerationLogGroupID(input.GroupID),
						RouteReason:       "allow_bound_account",
					}, nil
				} else if input.KeywordRouteResolver == nil && input.OriginalGroupAllowsBlockedKeywords && !input.KeywordRouteRequiresReconnect {
					log := s.buildLog(input, cfg, ContentModerationActionKeywordAllow, false, highestCategory, 1.0, scores, contentModerationMatchedExcerpt(content, keywordMatch.Keyword), nil, nil, "")
					log.MatchedKeyword = keywordMatch.Keyword
					applyContentPolicyLogEvidence(log, input, contentModerationLogGroupID(input.GroupID), input.GroupName, matchedSource, detectedLanguages, false, "allow_original")
					s.enqueueRecord(ctx, input, cfg, log, hashText, false, false)
					return &ContentModerationDecision{
						Allowed: true, Flagged: true, Action: ContentModerationActionKeywordAllow,
						MatchedKeyword: keywordMatch.Keyword, ContentSource: matchedSource,
						DetectedLanguages: detectedLanguages,
						OriginalGroupID:   contentModerationLogGroupID(input.GroupID),
						EffectiveGroupID:  contentModerationLogGroupID(input.GroupID),
						RouteReason:       "allow_original",
					}, nil
				} else if input.KeywordRouteResolver != nil {
					route, routeErr := input.KeywordRouteResolver(ctx, input.Model, input.Protocol, input.Body, cfg.KeywordGroupRoutingEnabled)
					if routeErr == nil && route != nil && route.Group != nil && route.Group.ID > 0 {
						if route.Group.ID == contentModerationLogGroupID(input.GroupID) {
							log := s.buildLog(input, cfg, ContentModerationActionKeywordAllow, false, highestCategory, 1.0, scores, contentModerationMatchedExcerpt(content, keywordMatch.Keyword), nil, nil, "")
							log.MatchedKeyword = keywordMatch.Keyword
							applyContentPolicyLogEvidence(log, input, route.Group.ID, route.Group.Name, matchedSource, detectedLanguages, false, "allow_unprotected_account")
							s.enqueueRecord(ctx, input, cfg, log, hashText, false, false)
							return &ContentModerationDecision{
								Allowed: true, Flagged: true, Action: ContentModerationActionKeywordAllow,
								MatchedKeyword: keywordMatch.Keyword, ContentSource: matchedSource,
								DetectedLanguages: detectedLanguages,
								OriginalGroupID:   route.Group.ID, EffectiveGroupID: route.Group.ID,
								RouteReason: "allow_unprotected_account", EffectiveGroup: route.Group,
								EffectiveSubscription: route.Subscription, ExcludedAccountIDs: route.ExcludedAccountIDs,
							}, nil
						}
						logInput := input
						actualGroupID := route.Group.ID
						logInput.GroupID, logInput.GroupName = &actualGroupID, route.Group.Name
						log := s.buildLog(logInput, cfg, ContentModerationActionKeywordReroute, false, highestCategory, 1.0, scores, contentModerationMatchedExcerpt(content, keywordMatch.Keyword), nil, nil, "")
						log.MatchedKeyword = keywordMatch.Keyword
						applyContentPolicyLogEvidence(log, input, route.Group.ID, route.Group.Name, matchedSource, detectedLanguages, true, "rerouted")
						s.enqueueRecord(ctx, logInput, cfg, log, hashText, false, false)
						return &ContentModerationDecision{
							Allowed: true, Flagged: true, Action: ContentModerationActionKeywordReroute,
							MatchedKeyword: keywordMatch.Keyword, ContentSource: matchedSource,
							DetectedLanguages: detectedLanguages,
							OriginalGroupID:   contentModerationLogGroupID(input.GroupID), EffectiveGroupID: route.Group.ID,
							Rerouted: true, RouteReason: "rerouted", EffectiveGroup: route.Group, EffectiveSubscription: route.Subscription,
							ExcludedAccountIDs: route.ExcludedAccountIDs,
						}, nil
					}
					if routeErr != nil {
						slog.Warn("content_moderation.keyword_route_failed", "request_id", input.RequestID, "error", routeErr)
					}
					// 无候选继续走下方稳定拦截逻辑，不能回退到严格分组。
				}
				s.recordPreBlockSyncMetric(0, ContentModerationActionKeywordBlock)
				// 只有最终拒绝且未发送给上游的轮次才进入历史隔离缓存；允许原组
				// 或成功切组的内容已经真实参与会话，不能在后续请求中被误删。
				isolationScopeID := input.IsolationScopeID
				if isolationScopeID == 0 {
					isolationScopeID = input.APIKeyID
				}
				if !input.DisableHistoryIsolation {
					s.recordBlockedTurnFingerprints(isolationScopeID, content, keywordMatch.Keyword)
				}
				slog.Info("content_moderation.keyword_block",
					"user_id", input.UserID,
					"api_key_id", input.APIKeyID,
					"group_id", contentModerationLogGroupID(input.GroupID),
					"endpoint", input.Endpoint,
					"protocol", input.Protocol,
					"keyword_blocking_mode", cfg.KeywordBlockingMode,
					"rule_pack_id", keywordMatch.PackID,
					"rule_pack_name", keywordMatch.PackName,
					"keyword", keywordMatch.Keyword)
				log := s.buildLog(input, cfg, ContentModerationActionKeywordBlock, true, highestCategory, 1.0, scores, contentModerationMatchedExcerpt(content, keywordMatch.Keyword), nil, nil, "")
				log.MatchedKeyword = keywordMatch.Keyword
				applyContentPolicyLogEvidence(log, input, contentModerationLogGroupID(input.GroupID), input.GroupName, matchedSource, detectedLanguages, false, keywordBlockRouteReason(input))
				s.enqueueRecord(ctx, input, cfg, log, hashText, false, true)
				return &ContentModerationDecision{
					Allowed:           false,
					Blocked:           true,
					Flagged:           true,
					Message:           cfg.BlockMessage,
					StatusCode:        cfg.BlockStatus,
					HighestCategory:   highestCategory,
					HighestScore:      1.0,
					CategoryScores:    scores,
					Action:            ContentModerationActionKeywordBlock,
					MatchedKeyword:    keywordMatch.Keyword,
					ContentSource:     matchedSource,
					DetectedLanguages: detectedLanguages,
					OriginalGroupID:   contentModerationLogGroupID(input.GroupID),
					EffectiveGroupID:  contentModerationLogGroupID(input.GroupID),
					RouteReason:       keywordBlockRouteReason(input),
				}, nil
			}
		}
		if cfg.KeywordBlockingMode == ContentModerationKeywordModeKeywordOnly {
			s.recordPreBlockSyncMetric(0, ContentModerationActionAllow)
			slog.Debug("content_moderation.skip_api_keyword_only",
				"user_id", input.UserID,
				"api_key_id", input.APIKeyID,
				"group_id", contentModerationLogGroupID(input.GroupID),
				"endpoint", input.Endpoint,
				"protocol", input.Protocol)
			return allow, nil
		}
	}
	if cfg.LocalKeywordOnly || input.LocalPolicyOnly {
		// 用户明确选择“只使用本地关键词”。这是安全边界而非性能优化：
		// 不读取历史命中 hash，不采样，也不允许任何配置把本轮内容发往第三方
		// moderation（内容审查）接口。未命中本地规则即直接放行。
		if cfg.Mode == ContentModerationModePreBlock {
			s.recordPreBlockSyncMetric(0, ContentModerationActionAllow)
		}
		slog.Debug("content_moderation.skip_remote_local_keyword_only",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return allow, nil
	}
	if cfg.PreHashCheckEnabled && s.hashCache != nil {
		matched, err := s.hashCache.HasFlaggedInputHash(ctx, hashText)
		if err != nil {
			slog.Warn("content_moderation.hash_check_failed", "user_id", input.UserID, "endpoint", input.Endpoint, "error", err)
		}
		if matched {
			if cfg.Mode == ContentModerationModePreBlock {
				s.recordPreBlockSyncMetric(0, ContentModerationActionHashBlock)
			}
			slog.Info("content_moderation.hash_block",
				"user_id", input.UserID,
				"api_key_id", input.APIKeyID,
				"group_id", contentModerationLogGroupID(input.GroupID),
				"endpoint", input.Endpoint,
				"protocol", input.Protocol,
				"input_hash", hashText)
			message := cfg.BlockMessage
			if message != "" {
				message = fmt.Sprintf("%s（hash: %s）", message, hashText)
			}
			scores := map[string]float64{"hash": 1.0}
			log := s.buildLog(input, cfg, ContentModerationActionHashBlock, true, "hash", 1.0, scores, content.ExcerptText(), nil, nil, "")
			s.enqueueRecord(ctx, input, cfg, log, hashText, false, false)
			return &ContentModerationDecision{
				Allowed:    false,
				Blocked:    true,
				Flagged:    true,
				Message:    message,
				StatusCode: cfg.BlockStatus,
				InputHash:  hashText,
				Action:     ContentModerationActionHashBlock,
			}, nil
		}
	}
	if !cfg.shouldSample(hashText) {
		if cfg.Mode == ContentModerationModePreBlock {
			s.recordPreBlockSyncMetric(0, ContentModerationActionAllow)
		}
		slog.Info("content_moderation.skip_sample_rate",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"sample_rate", cfg.SampleRate)
		return allow, nil
	}
	if len(cfg.apiKeys()) == 0 {
		if cfg.Mode == ContentModerationModePreBlock {
			s.recordPreBlockSyncMetric(0, ContentModerationActionError)
		}
		slog.Warn("content_moderation.skip_no_audit_api_keys",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol)
		return allow, nil
	}
	// 所有客户端系统指令只进入本网关的本地语种与关键词自动机。这些控制
	// 消息可能含客户端密钥或私有提示词，不能因为兼容旧的第三方 moderation
	// API（内容审查接口）而扩大外发范围；普通用户/Skill/工具内容保持不变。
	remoteContent := contentModerationRemoteInput(content)
	if remoteContent.IsEmpty() {
		return allow, nil
	}
	if cfg.Mode == ContentModerationModeObserve {
		slog.Info("content_moderation.enqueue_observe",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"queue_len", len(s.asyncQueue))
		s.enqueueAsync(input, cfg, remoteContent, hashText)
		return allow, nil
	}

	return s.checkSync(ctx, input, cfg, remoteContent, hashText, nil, true), nil
}

func (s *ContentModerationService) checkSync(ctx context.Context, input ContentModerationCheckInput, cfg *ContentModerationConfig, content ContentModerationInput, hashText string, queueDelay *int, allowBlock bool) *ContentModerationDecision {
	allow := &ContentModerationDecision{Allowed: true, Action: ContentModerationActionAllow}
	trackPreBlock := queueDelay == nil && allowBlock && cfg != nil && cfg.Mode == ContentModerationModePreBlock
	if trackPreBlock {
		s.preBlockActive.Add(1)
		defer s.preBlockActive.Add(-1)
	}
	start := time.Now()
	result, err := s.callModeration(ctx, cfg, content.ModerationInput(), trackPreBlock)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		if trackPreBlock {
			s.recordPreBlockSyncMetric(latency, ContentModerationActionError)
		}
		slog.Warn("content_moderation.audit_api_failed",
			"user_id", input.UserID,
			"api_key_id", input.APIKeyID,
			"group_id", contentModerationLogGroupID(input.GroupID),
			"endpoint", input.Endpoint,
			"protocol", input.Protocol,
			"mode", cfg.Mode,
			"allow_block", allowBlock,
			"queue_delay_ms", queueDelay,
			"latency_ms", latency,
			"error", err)
		if queueDelay != nil {
			s.asyncErrors.Add(1)
		}
		if cfg.RecordNonHits {
			log := s.buildLog(input, cfg, ContentModerationActionError, false, "", 0, nil, content.ExcerptText(), &latency, queueDelay, err.Error())
			_ = s.repo.CreateLog(ctx, log)
		}
		return allow
	}

	flagged, highestCategory, highestScore := evaluateModerationScores(result.CategoryScores, cfg.Thresholds)
	action := ContentModerationActionAllow
	blocked := false
	if allowBlock && flagged && cfg.Mode == ContentModerationModePreBlock {
		action = ContentModerationActionBlock
		blocked = true
	}
	if trackPreBlock {
		s.recordPreBlockSyncMetric(latency, action)
	}
	slog.Info("content_moderation.audit_result",
		"user_id", input.UserID,
		"api_key_id", input.APIKeyID,
		"group_id", contentModerationLogGroupID(input.GroupID),
		"group_name", input.GroupName,
		"endpoint", input.Endpoint,
		"protocol", input.Protocol,
		"mode", cfg.Mode,
		"allow_block", allowBlock,
		"flagged", flagged,
		"blocked", blocked,
		"action", action,
		"highest_category", highestCategory,
		"highest_score", highestScore,
		"latency_ms", latency,
		"queue_delay_ms", queueDelay)
	if flagged || cfg.RecordNonHits {
		log := s.buildLog(input, cfg, action, flagged, highestCategory, highestScore, result.CategoryScores, content.ExcerptText(), &latency, queueDelay, "")
		if queueDelay == nil && cfg.Mode == ContentModerationModePreBlock {
			s.enqueueRecord(ctx, input, cfg, log, hashText, flagged, flagged)
		} else {
			s.persistContentModerationLog(ctx, cfg, log, hashText, flagged, flagged)
		}
	}
	if blocked {
		return &ContentModerationDecision{
			Allowed:         false,
			Blocked:         true,
			Flagged:         true,
			Message:         cfg.BlockMessage,
			StatusCode:      cfg.BlockStatus,
			HighestCategory: highestCategory,
			HighestScore:    highestScore,
			CategoryScores:  result.CategoryScores,
			Action:          action,
		}
	}
	return &ContentModerationDecision{
		Allowed:         true,
		Flagged:         flagged,
		Message:         "",
		HighestCategory: highestCategory,
		HighestScore:    highestScore,
		CategoryScores:  result.CategoryScores,
		Action:          action,
	}
}

func (s *ContentModerationService) recordPreBlockSyncMetric(latencyMS int, action string) {
	if s == nil {
		return
	}
	s.preBlockChecked.Add(1)
	if latencyMS < 0 {
		latencyMS = 0
	}
	s.preBlockLatencyTotalMS.Add(int64(latencyMS))
	switch action {
	case ContentModerationActionBlock, ContentModerationActionHashBlock, ContentModerationActionKeywordBlock, ContentModerationActionLanguageBlock:
		s.preBlockBlocked.Add(1)
	case ContentModerationActionError:
		s.preBlockErrors.Add(1)
	default:
		s.preBlockAllowed.Add(1)
	}
}

func (s *ContentModerationService) enqueueAsync(input ContentModerationCheckInput, cfg *ContentModerationConfig, content ContentModerationInput, hashText string) {
	if s == nil || s.asyncQueue == nil {
		return
	}
	queueSize := defaultContentModerationQueueSize
	if cfg != nil && cfg.QueueSize > 0 {
		queueSize = cfg.QueueSize
	}
	if len(s.asyncQueue) >= queueSize {
		slog.Warn("content_moderation.async_queue_full", "user_id", input.UserID, "endpoint", input.Endpoint, "queue_size", queueSize)
		s.asyncDropped.Add(1)
		return
	}
	task := contentModerationTask{
		input:      input,
		content:    content,
		inputHash:  hashText,
		enqueuedAt: time.Now(),
	}
	select {
	case s.asyncQueue <- task:
		s.asyncEnqueued.Add(1)
	default:
		slog.Warn("content_moderation.async_queue_full", "user_id", input.UserID, "endpoint", input.Endpoint)
		s.asyncDropped.Add(1)
	}
}

func (s *ContentModerationService) enqueueRecord(ctx context.Context, input ContentModerationCheckInput, cfg *ContentModerationConfig, log *ContentModerationLog, inputHash string, recordHash bool, applySideEffects bool) {
	if s == nil || s.asyncQueue == nil || log == nil {
		return
	}
	if applySideEffects {
		// 扣款、处罚累计和企业成员暂停是关键状态，不能与普通审计日志共用
		// “队列满可丢弃”的策略。只在真实强拦截时同步确认主站已接收或事件
		// 已进入 durable outbox（可靠投递箱）；未命中和只审计仍走异步队列。
		s.persistContentModerationLog(ctx, cfg, log, inputHash, recordHash, true)
		return
	}
	queueSize := defaultContentModerationQueueSize
	if cfg != nil && cfg.QueueSize > 0 {
		queueSize = cfg.QueueSize
	}
	if len(s.asyncQueue) >= queueSize {
		slog.Warn("content_moderation.record_queue_full",
			"user_id", input.UserID,
			"endpoint", input.Endpoint,
			"action", log.Action,
			"queue_size", queueSize)
		s.asyncDropped.Add(1)
		return
	}
	task := contentModerationTask{
		input:            input,
		inputHash:        inputHash,
		log:              log,
		config:           cloneContentModerationConfig(cfg),
		recordHash:       recordHash,
		applySideEffects: applySideEffects,
		enqueuedAt:       time.Now(),
	}
	select {
	case s.asyncQueue <- task:
		s.asyncEnqueued.Add(1)
	default:
		slog.Warn("content_moderation.record_queue_full",
			"user_id", input.UserID,
			"endpoint", input.Endpoint,
			"action", log.Action)
		s.asyncDropped.Add(1)
	}
}

func (s *ContentModerationService) worker(id int) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), maxContentModerationTimeoutMS*time.Millisecond+10*time.Second)
		// Workers share the same immutable runtime snapshot as the request path.
		// Loading and normalizing the large built-in keyword packs once per worker
		// used to turn 32 idle workers into 32 settings queries and normalizations
		// every second. The snapshot keeps configuration changes within the same
		// short TTL while single-flight refresh prevents an idle CPU/DB hot loop.
		runtimeSnapshot, err := s.loadRuntimeSnapshot(ctx)
		if err != nil || runtimeSnapshot == nil || runtimeSnapshot.config == nil {
			cancel()
			time.Sleep(time.Second)
			continue
		}
		cfg := runtimeSnapshot.config
		if id >= cfg.WorkerCount {
			cancel()
			time.Sleep(time.Second)
			continue
		}
		task, ok := s.dequeueAsyncTask(ctx, time.Second)
		if !ok {
			cancel()
			continue
		}
		func() {
			defer cancel()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("content_moderation.worker_panic", "worker_id", id, "recover", r)
				}
			}()
			if task.log != nil {
				s.asyncActive.Add(1)
				defer s.asyncActive.Add(-1)
				queueDelay := int(time.Since(task.enqueuedAt).Milliseconds())
				task.log.QueueDelayMS = &queueDelay
				taskCfg := task.config
				if taskCfg == nil {
					taskCfg = cfg
				}
				s.persistContentModerationLog(ctx, taskCfg, task.log, task.inputHash, task.recordHash, task.applySideEffects)
				s.asyncProcessed.Add(1)
				return
			}
			if !cfg.Enabled || cfg.Mode == ContentModerationModeOff || len(cfg.apiKeys()) == 0 {
				return
			}
			if !cfg.includesGroup(task.input.GroupID) {
				return
			}
			if !cfg.includesModel(task.input.Model) {
				return
			}
			s.asyncActive.Add(1)
			defer s.asyncActive.Add(-1)
			queueDelay := int(time.Since(task.enqueuedAt).Milliseconds())
			_ = s.checkSync(ctx, task.input, cfg, task.content, task.inputHash, &queueDelay, false)
			s.asyncProcessed.Add(1)
		}()
	}
}

func (s *ContentModerationService) dequeueAsyncTask(ctx context.Context, idleWait time.Duration) (contentModerationTask, bool) {
	var zero contentModerationTask
	if s == nil || s.asyncQueue == nil {
		return zero, false
	}
	if idleWait <= 0 {
		idleWait = time.Second
	}
	timer := time.NewTimer(idleWait)
	defer timer.Stop()
	select {
	case task, ok := <-s.asyncQueue:
		return task, ok
	case <-ctx.Done():
		return zero, false
	case <-timer.C:
		return zero, false
	}
}

func (s *ContentModerationService) ListLogs(ctx context.Context, filter ContentModerationLogFilter) ([]ContentModerationLog, *pagination.PaginationResult, error) {
	if filter.Pagination.Page <= 0 {
		filter.Pagination.Page = 1
	}
	if filter.Pagination.PageSize <= 0 {
		filter.Pagination.PageSize = 20
	}
	if filter.Pagination.PageSize > 100 {
		filter.Pagination.PageSize = 100
	}
	if filter.Pagination.SortOrder == "" {
		filter.Pagination.SortOrder = pagination.SortOrderDesc
	}
	return s.repo.ListLogs(ctx, filter)
}

func (s *ContentModerationService) UnbanUser(ctx context.Context, userID int64) (*ContentModerationUnbanUserResult, error) {
	if s == nil || s.userRepo == nil {
		return nil, infraerrors.InternalServer("CONTENT_MODERATION_USER_REPOSITORY_UNAVAILABLE", "用户仓储不可用")
	}
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_USER_ID", "用户 ID 无效")
	}
	if s.repo != nil {
		permanentlyBanned, err := s.repo.IsUserPermanentlyBanned(ctx, userID)
		if err != nil {
			return nil, err
		}
		if permanentlyBanned {
			return nil, infraerrors.Conflict("CONTENT_MODERATION_USER_PERMANENTLY_BANNED", "用户已被内容审计永久封禁，不能解封")
		}
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, infraerrors.NotFound("USER_NOT_FOUND", "用户不存在")
		}
		return nil, fmt.Errorf("get content moderation unban user: %w", err)
	}
	if user.Status != StatusActive {
		user.Status = StatusActive
		if err := s.userRepo.Update(ctx, user, UserUpdateFields{Status: true}); err != nil {
			return nil, fmt.Errorf("update content moderation unban user: %w", err)
		}
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return &ContentModerationUnbanUserResult{
		UserID: userID,
		Status: StatusActive,
	}, nil
}

func (s *ContentModerationService) DeleteFlaggedInputHash(ctx context.Context, inputHash string) (*ContentModerationDeleteHashResult, error) {
	inputHash = normalizeContentModerationHash(inputHash)
	if inputHash == "" {
		return nil, infraerrors.BadRequest("INVALID_CONTENT_MODERATION_HASH", "风险输入哈希无效")
	}
	if s == nil || s.hashCache == nil {
		return nil, infraerrors.InternalServer("CONTENT_MODERATION_HASH_CACHE_UNAVAILABLE", "内容审计哈希缓存不可用")
	}
	deleted, err := s.hashCache.DeleteFlaggedInputHash(ctx, inputHash)
	if err != nil {
		return nil, fmt.Errorf("delete content moderation flagged hash: %w", err)
	}
	return &ContentModerationDeleteHashResult{
		InputHash: inputHash,
		Deleted:   deleted,
	}, nil
}

func (s *ContentModerationService) ClearFlaggedInputHashes(ctx context.Context) (*ContentModerationClearHashesResult, error) {
	if s == nil || s.hashCache == nil {
		return nil, infraerrors.InternalServer("CONTENT_MODERATION_HASH_CACHE_UNAVAILABLE", "内容审计哈希缓存不可用")
	}
	deleted, err := s.hashCache.ClearFlaggedInputHashes(ctx)
	if err != nil {
		return nil, fmt.Errorf("clear content moderation flagged hashes: %w", err)
	}
	return &ContentModerationClearHashesResult{Deleted: deleted}, nil
}

func (s *ContentModerationService) GetStatus(ctx context.Context) (*ContentModerationRuntimeStatus, error) {
	if s == nil {
		return &ContentModerationRuntimeStatus{}, nil
	}
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	riskEnabled := s.isRiskControlEnabled(ctx)
	active := int(s.asyncActive.Load())
	if active < 0 {
		active = 0
	}
	if active > cfg.WorkerCount {
		active = cfg.WorkerCount
	}
	preBlockActive := int(s.preBlockActive.Load())
	if preBlockActive < 0 {
		preBlockActive = 0
	}
	preBlockChecked := s.preBlockChecked.Load()
	preBlockAvgLatency := int64(0)
	if preBlockChecked > 0 {
		preBlockAvgLatency = s.preBlockLatencyTotalMS.Load() / preBlockChecked
	}
	queueLength := 0
	if s.asyncQueue != nil {
		queueLength = len(s.asyncQueue)
	}
	queueUsage := 0.0
	if cfg.QueueSize > 0 {
		queueUsage = float64(queueLength) * 100 / float64(cfg.QueueSize)
	}
	var flaggedHashCount int64
	if s.hashCache != nil {
		if n, err := s.hashCache.CountFlaggedInputHashes(ctx); err == nil {
			flaggedHashCount = n
		} else {
			slog.Warn("content_moderation.hash_count_failed", "error", err)
		}
	}
	var lastCleanupAt *time.Time
	if unix := s.lastCleanupUnix.Load(); unix > 0 {
		t := time.Unix(unix, 0)
		lastCleanupAt = &t
	}
	aiMetrics, _ := s.GetAIMetrics(ctx)
	return &ContentModerationRuntimeStatus{
		AI:                           aiMetrics,
		Enabled:                      cfg.Enabled,
		RiskControlEnabled:           riskEnabled,
		Mode:                         cfg.Mode,
		WorkerCount:                  cfg.WorkerCount,
		MaxWorkers:                   maxContentModerationWorkerCount,
		ActiveWorkers:                active,
		IdleWorkers:                  cfg.WorkerCount - active,
		QueueSize:                    cfg.QueueSize,
		QueueLength:                  queueLength,
		QueueUsagePercent:            queueUsage,
		Enqueued:                     s.asyncEnqueued.Load(),
		Dropped:                      s.asyncDropped.Load(),
		Processed:                    s.asyncProcessed.Load(),
		Errors:                       s.asyncErrors.Load(),
		PreBlockActive:               preBlockActive,
		PreBlockChecked:              preBlockChecked,
		PreBlockAllowed:              s.preBlockAllowed.Load(),
		PreBlockBlocked:              s.preBlockBlocked.Load(),
		PreBlockErrors:               s.preBlockErrors.Load(),
		PreBlockAvgLatencyMS:         preBlockAvgLatency,
		PreBlockAPIKeyActive:         s.preBlockAPIKeyActive(cfg.apiKeys()),
		PreBlockAPIKeyAvailableCount: s.preBlockAPIKeyAvailableCount(cfg.apiKeys()),
		PreBlockAPIKeyTotalCalls:     s.preBlockAPIKeyTotalCalls(cfg.apiKeys()),
		PreBlockAPIKeyLoads:          s.preBlockAPIKeyLoads(cfg.apiKeys()),
		APIKeyStatuses:               s.apiKeyStatuses(cfg.apiKeys()),
		FlaggedHashCount:             flaggedHashCount,
		LastCleanupAt:                lastCleanupAt,
		LastCleanupDeletedHit:        s.lastCleanupDeletedHit.Load(),
		LastCleanupDeletedNonHit:     s.lastCleanupDeletedNonHit.Load(),
	}, nil
}

// ensureWorkers 把已启动的 worker 数补齐到 target（只增不减）。
//
// 只增不减的理由：worker() 自身有 id 门控（id >= 运行时配置的 cfg.WorkerCount 就空转等待），
// 「配置调小」已被既有逻辑覆盖，所以这里不需要回收 id，也就不存在 worker 反复增删的抖动。
func (s *ContentModerationService) ensureWorkers(target int) {
	if s == nil {
		return
	}
	if target > maxContentModerationWorkerCount {
		target = maxContentModerationWorkerCount
	}
	if target < 0 {
		target = 0
	}
	s.workerSpawnMu.Lock()
	defer s.workerSpawnMu.Unlock()
	for s.workerCount < target {
		go s.worker(s.workerCount)
		s.workerCount++
	}
}

// workerSupervisor 周期性地把 worker 数补齐到设置里的 worker_count。
//
// 历史形态是构造时无条件起 maxContentModerationWorkerCount(32) 个 goroutine，而生产
// content_moderation_config.worker_count = 4 —— 多出来的 28 个 goroutine 只会在
// 「读快照 → 睡 1s」之间空转（与队列深度那次修复同源：为「运行时可能调大」预留了用不到的资源）。
// 现在按默认值起，运维把 worker_count 调大时由这里补起（最多晚一个周期生效）。
func (s *ContentModerationService) workerSupervisor() {
	ticker := time.NewTicker(contentModerationWorkerReconcileInterval)
	defer ticker.Stop()
	for {
		<-ticker.C
		s.reconcileWorkerCount()
	}
}

// reconcileWorkerCount 读取运行时快照（TTL 内走缓存，失败即放弃本次补齐）并把 worker 数补齐。
func (s *ContentModerationService) reconcileWorkerCount() {
	if s == nil || s.settingRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contentModerationWorkerReconcileTimeout)
	defer cancel()
	snapshot, err := s.loadRuntimeSnapshot(ctx)
	if err != nil || snapshot == nil || snapshot.config == nil {
		return
	}
	target := snapshot.config.WorkerCount
	if target <= 0 {
		target = defaultContentModerationWorkerCount
	}
	s.ensureWorkers(target)
}

func (s *ContentModerationService) cleanupWorker() {
	timer := time.NewTimer(contentModerationCleanupDelay)
	defer timer.Stop()
	for {
		<-timer.C
		s.runCleanupOnce()
		timer.Reset(contentModerationCleanupInterval)
	}
}

func (s *ContentModerationService) runCleanupOnce() {
	if s == nil || s.repo == nil || s.settingRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contentModerationCleanupTimeout)
	defer cancel()
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		slog.Warn("content_moderation.cleanup_load_config_failed", "error", err)
		return
	}
	now := time.Now()
	hitBefore := now.AddDate(0, 0, -cfg.HitRetentionDays)
	nonHitBefore := now.AddDate(0, 0, -cfg.NonHitRetentionDays)
	result, err := s.repo.CleanupExpiredLogs(ctx, hitBefore, nonHitBefore)
	if err != nil {
		slog.Warn("content_moderation.cleanup_failed", "error", err)
		return
	}
	if result == nil {
		return
	}
	s.lastCleanupUnix.Store(result.FinishedAt.Unix())
	s.lastCleanupDeletedHit.Store(result.DeletedHit)
	s.lastCleanupDeletedNonHit.Store(result.DeletedNonHit)
}

func (s *ContentModerationService) loadConfig(ctx context.Context) (*ContentModerationConfig, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyContentModerationConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			cfg, parseErr := parseContentModerationConfig("")
			if parseErr == nil {
				s.applyKeywordCatalog(cfg)
			}
			return cfg, parseErr
		}
		return nil, fmt.Errorf("get content moderation config: %w", err)
	}
	cfg, err := parseContentModerationConfig(raw)
	if err != nil {
		return nil, err
	}
	s.applyKeywordCatalog(cfg)
	return cfg, nil
}

func (s *ContentModerationService) keywordCatalogDigest() string {
	if s == nil || s.keywordCatalog == nil {
		return ""
	}
	snapshot := s.keywordCatalog.currentSnapshot()
	if snapshot == nil {
		return ""
	}
	return snapshot.Digest
}

func (s *ContentModerationService) applyKeywordCatalog(cfg *ContentModerationConfig) {
	if s == nil || cfg == nil || s.keywordCatalog == nil {
		return
	}
	snapshot := s.keywordCatalog.currentSnapshot()
	if snapshot == nil || len(snapshot.Packs) == 0 {
		return
	}
	cfg.KeywordRulePacks = mergeKeywordCatalogWithLocalDeltas(
		snapshot.Packs,
		cfg.KeywordRulePackOverrides,
		cfg.KeywordRulePacks,
		cfg.KeywordCatalogOverrideVersion,
	)
	cfg.KeywordRulePackOverrides = normalizeContentModerationKeywordRulePackOverrides(cfg.KeywordRulePackOverrides)
}

func (s *ContentModerationService) keywordCatalogBasePacks() []ContentModerationKeywordRulePack {
	if s != nil && s.keywordCatalog != nil {
		if snapshot := s.keywordCatalog.currentSnapshot(); snapshot != nil && len(snapshot.Packs) > 0 {
			return cloneContentModerationKeywordRulePacks(snapshot.Packs)
		}
	}
	return defaultContentModerationKeywordRulePacks()
}

func (s *ContentModerationService) contentModerationConfigForStorage(cfg *ContentModerationConfig) *ContentModerationConfig {
	stored := cloneContentModerationConfig(cfg)
	base := s.keywordCatalogBasePacks()
	baseIDs := make(map[string]struct{}, len(base))
	for _, pack := range base {
		baseIDs[normalizeContentModerationRulePackID(pack.ID)] = struct{}{}
	}
	custom := make([]ContentModerationKeywordRulePack, 0)
	for _, pack := range stored.KeywordRulePacks {
		id := normalizeContentModerationRulePackID(pack.ID)
		if id == "" || pack.BuiltIn {
			continue
		}
		if _, builtIn := baseIDs[id]; builtIn {
			continue
		}
		pack.ID = id
		pack.Keywords = normalizeBlockedKeywords(pack.Keywords)
		pack.Action = normalizeContentModerationKeywordRuleAction(pack.Action)
		if len(pack.Keywords) == 0 || pack.Action == "" {
			continue
		}
		custom = append(custom, pack)
	}
	stored.KeywordRulePacks = custom
	stored.KeywordRulePackOverrides = normalizeContentModerationKeywordRulePackOverrides(stored.KeywordRulePackOverrides)
	stored.KeywordCatalogOverrideVersion = 1
	return stored
}

func parseContentModerationConfig(raw string) (*ContentModerationConfig, error) {
	cfg := defaultContentModerationConfig()
	if strings.TrimSpace(raw) == "" {
		cfg.normalize()
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, infraerrors.BadRequest("INVALID_CONTENT_MODERATION_CONFIG", "内容审计配置不是有效 JSON")
	}
	cfg.normalize()
	return cfg, nil
}

func (s *ContentModerationService) loadRuntimeSnapshot(ctx context.Context) (*contentModerationRuntimeSnapshot, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("content moderation setting repository unavailable")
	}
	now := time.Now()
	if snapshot := s.runtimeSnapshot.Load(); snapshot != nil {
		if now.Sub(snapshot.loadedAt) < s.runtimeSnapshotTTL() && snapshot.catalogDigest == s.keywordCatalogDigest() {
			return snapshot, nil
		}
		s.triggerRuntimeSnapshotRefresh()
		return snapshot, nil
	}
	if s.runtimeRefreshDeferred() {
		return nil, errors.New("content moderation runtime snapshot refresh deferred after a recent failure")
	}

	s.runtimeRefreshMu.Lock()
	defer s.runtimeRefreshMu.Unlock()
	if snapshot := s.runtimeSnapshot.Load(); snapshot != nil {
		return snapshot, nil
	}
	// Callers may all have passed the fast-path check before the first failed
	// refresh acquired the mutex. Recheck the retry gate while holding the lock
	// so a database outage still produces at most one read attempt per TTL.
	if s.runtimeRefreshDeferred() {
		return nil, errors.New("content moderation runtime snapshot refresh deferred after a recent failure")
	}
	snapshot, err := s.refreshRuntimeSnapshot(ctx)
	if err != nil {
		s.runtimeRefreshRetryAt.Store(time.Now().Add(s.runtimeSnapshotTTL()).UnixNano())
		return nil, err
	}
	return snapshot, nil
}

func (s *ContentModerationService) runtimeSnapshotTTL() time.Duration {
	if s != nil && s.runtimeCacheTTL > 0 {
		return s.runtimeCacheTTL
	}
	return contentModerationRuntimeCacheTTL
}

func (s *ContentModerationService) triggerRuntimeSnapshotRefresh() {
	if s == nil || s.runtimeRefreshDeferred() || !s.runtimeRefreshMu.TryLock() {
		return
	}
	if s.runtimeRefreshDeferred() {
		s.runtimeRefreshMu.Unlock()
		return
	}
	go func() {
		defer s.runtimeRefreshMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), contentModerationRuntimeRefreshTimeout)
		defer cancel()
		if _, err := s.refreshRuntimeSnapshot(ctx); err != nil {
			s.runtimeRefreshRetryAt.Store(time.Now().Add(s.runtimeSnapshotTTL()).UnixNano())
			slog.Warn("content_moderation.runtime_snapshot_refresh_failed", "error", err)
		}
	}()
}

func (s *ContentModerationService) runtimeRefreshDeferred() bool {
	if s == nil {
		return false
	}
	return time.Now().UnixNano() < s.runtimeRefreshRetryAt.Load()
}

func (s *ContentModerationService) refreshRuntimeSnapshot(ctx context.Context) (*contentModerationRuntimeSnapshot, error) {
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyRiskControlEnabled,
		SettingKeyContentModerationConfig,
		SettingKeyCyberPolicyUserAllowlist,
	})
	if err != nil {
		return nil, fmt.Errorf("get content moderation runtime settings: %w", err)
	}
	// 白名单非法/读取失败时保持「空白名单」而不是沿用旧值：风控宁可多拦一个，
	// 也不能因为配置写坏就静默放行（与 GetCyberSessionBlockRuntime 的降级策略
	// 不同——那里旧值仍然可用，这里直接放弃豁免）。
	allowlistedUsers, allowlistErr := ParseCyberPolicyUserAllowlist(values[SettingKeyCyberPolicyUserAllowlist])
	if allowlistErr != nil {
		slog.Warn("content_moderation.cyber_allowlist_parse_failed", "error", allowlistErr)
		allowlistedUsers = nil
	}
	rawConfig := values[SettingKeyContentModerationConfig]
	configDigest := sha256.Sum256([]byte(rawConfig))
	catalogDigest := s.keywordCatalogDigest()
	if current := s.runtimeSnapshot.Load(); current != nil && current.configDigest == configDigest && current.catalogDigest == catalogDigest {
		snapshot := &contentModerationRuntimeSnapshot{
			riskControlEnabled: values[SettingKeyRiskControlEnabled] == "true",
			config:             current.config,
			keywordRuleMatcher: current.keywordRuleMatcher,
			allowlistedUsers:   allowlistedUsers,
			configDigest:       configDigest,
			catalogDigest:      catalogDigest,
			loadedAt:           time.Now(),
		}
		s.runtimeSnapshot.Store(snapshot)
		s.runtimeRefreshRetryAt.Store(0)
		return snapshot, nil
	}
	cfg, err := parseContentModerationConfig(rawConfig)
	if err != nil {
		return nil, err
	}
	s.applyKeywordCatalog(cfg)
	snapshot := &contentModerationRuntimeSnapshot{
		riskControlEnabled: values[SettingKeyRiskControlEnabled] == "true",
		config:             cfg,
		keywordRuleMatcher: newContentModerationKeywordRuleMatcher(cfg),
		allowlistedUsers:   allowlistedUsers,
		configDigest:       configDigest,
		catalogDigest:      catalogDigest,
		loadedAt:           time.Now(),
	}
	s.runtimeSnapshot.Store(snapshot)
	s.runtimeRefreshRetryAt.Store(0)
	return snapshot, nil
}

func (s *ContentModerationService) replaceRuntimeConfig(cfg *ContentModerationConfig, raw []byte) {
	if s == nil || cfg == nil {
		return
	}
	s.runtimeRefreshMu.Lock()
	hasSnapshot := s.runtimeSnapshot.Load() != nil
	s.runtimeRefreshMu.Unlock()
	if !hasSnapshot {
		return
	}

	config := cloneContentModerationConfig(cfg)
	s.applyKeywordCatalog(config)
	keywordRuleMatcher := newContentModerationKeywordRuleMatcher(config)
	configDigest := sha256.Sum256(raw)
	catalogDigest := s.keywordCatalogDigest()

	s.runtimeRefreshMu.Lock()
	defer s.runtimeRefreshMu.Unlock()
	current := s.runtimeSnapshot.Load()
	if current == nil {
		return
	}
	riskControlEnabled := current.riskControlEnabled
	if config.Enabled {
		riskControlEnabled = true
	}
	s.runtimeSnapshot.Store(&contentModerationRuntimeSnapshot{
		riskControlEnabled: riskControlEnabled,
		config:             config,
		keywordRuleMatcher: keywordRuleMatcher,
		// 在线改配置不应丢掉白名单（它来自另一个 setting key，不受本次配置影响）。
		allowlistedUsers: current.allowlistedUsers,
		configDigest:     configDigest,
		catalogDigest:    catalogDigest,
		loadedAt:         time.Now(),
	})
	s.runtimeRefreshRetryAt.Store(0)
}

func (s *ContentModerationService) isRiskControlEnabled(ctx context.Context) bool {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyRiskControlEnabled)
	if err != nil {
		return false
	}
	return raw == "true"
}

func (s *ContentModerationService) validateConfig(ctx context.Context, cfg *ContentModerationConfig) error {
	if cfg == nil {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_CONFIG", "内容审计配置不能为空")
	}
	cfg.normalize()
	switch cfg.Mode {
	case ContentModerationModeOff, ContentModerationModeObserve, ContentModerationModePreBlock, ContentModerationModeShadow:
	default:
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_MODE", "内容审计模式无效")
	}
	if err := cfg.AI.validate(); err != nil {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_AI_CONFIG", err.Error())
	}
	if cfg.KeywordBlockingMode == ContentModerationKeywordModeAIOnly && cfg.Mode == ContentModerationModeObserve {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_AI_MODE", "纯 AI 请选择无处罚影子模式或同步阻断")
	}
	if cfg.KeywordBlockingMode != ContentModerationKeywordModeAIOnly && cfg.Mode == ContentModerationModeShadow {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_AI_MODE", "影子模式仅适用于纯 AI")
	}
	if cfg.KeywordBlockingMode == ContentModerationKeywordModeAIOnly && cfg.AI.PenaltiesEnabled && cfg.AutoBanEnabled && !cfg.PermanentBanEnabled {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_AI_PENALTY", "当前主站未实现独立临时封禁，请明确配置永久封禁或企业暂停策略")
	}
	if _, err := url.ParseRequestURI(cfg.BaseURL); err != nil {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_BASE_URL", "OpenAI Base URL 无效")
	}
	if cfg.BlockStatus < 400 || cfg.BlockStatus > 599 {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_BLOCK_STATUS", "拦截 HTTP 状态码必须在 400-599 之间")
	}
	if cfg.ModelFilter.Type != ContentModerationModelFilterAll && len(cfg.ModelFilter.Models) == 0 {
		return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_MODEL_FILTER", "指定或排除模型时至少需要配置 1 个模型")
	}
	if !cfg.AllGroups && len(cfg.GroupIDs) > 0 && s.groupRepo != nil {
		for _, groupID := range cfg.GroupIDs {
			if _, err := s.groupRepo.GetByIDLite(ctx, groupID); err != nil {
				return infraerrors.BadRequest("INVALID_CONTENT_MODERATION_GROUP", fmt.Sprintf("审计分组不存在: %d", groupID))
			}
		}
	}
	return nil
}

func (s *ContentModerationService) callModeration(ctx context.Context, cfg *ContentModerationConfig, input any, trackKeyLoad ...bool) (*moderationAPIResult, error) {
	attempts := cfg.RetryCount + 1
	if attempts <= 0 {
		attempts = 1
	}
	if attempts > maxContentModerationRetryCount+1 {
		attempts = maxContentModerationRetryCount + 1
	}
	trackLoad := len(trackKeyLoad) > 0 && trackKeyLoad[0]
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		key, ok := s.nextUsableAPIKey(cfg)
		if !ok {
			lastErr = errors.New("no moderation api key available")
			break
		}
		if trackLoad {
			s.beginModerationAPIKeyCall(key)
		}
		start := time.Now()
		httpStatus := 0
		result, err := s.callModerationOnceWithInput(ctx, cfg, key, input, &httpStatus)
		latency := int(time.Since(start).Milliseconds())
		if err == nil {
			if trackLoad {
				s.finishModerationAPIKeyCall(key, latency, true)
			}
			s.markAPIKeySuccess(key, latency, httpStatus)
			return result, nil
		}
		if trackLoad {
			s.finishModerationAPIKeyCall(key, latency, false)
		}
		s.markAPIKeyError(key, err.Error(), latency, httpStatus)
		lastErr = err
		if httpStatus == http.StatusBadRequest {
			break
		}
		if attempt == attempts-1 {
			break
		}
		wait := time.Duration(100*(attempt+1)) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, lastErr
}

func (s *ContentModerationService) callModerationOnceWithInput(ctx context.Context, cfg *ContentModerationConfig, apiKey string, input any, httpStatus *int) (*moderationAPIResult, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	endpoint, err := url.JoinPath(base, "/v1/moderations")
	if err != nil {
		return nil, err
	}
	payload := moderationAPIRequest{
		Model: cfg.Model,
		Input: input,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if httpStatus != nil {
		*httpStatus = resp.StatusCode
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("moderation api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out moderationAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, errors.New("moderation api returned empty results")
	}
	return &out.Results[0], nil
}

func (s *ContentModerationService) buildLog(input ContentModerationCheckInput, cfg *ContentModerationConfig, action string, flagged bool, highestCategory string, highestScore float64, scores map[string]float64, text string, latency *int, queueDelay *int, errText string) *ContentModerationLog {
	var userID *int64
	if input.UserID > 0 {
		userID = &input.UserID
	}
	var apiKeyID *int64
	if input.APIKeyID > 0 {
		apiKeyID = &input.APIKeyID
	}
	return &ContentModerationLog{
		RequestID:          input.RequestID,
		UserID:             userID,
		UserEmail:          input.UserEmail,
		OrganizationID:     input.OrganizationID,
		BillingSubjectType: strings.TrimSpace(input.BillingSubjectType),
		BillingSubjectID:   input.BillingSubjectID,
		ControlPlaneKeyID:  strings.TrimSpace(input.ControlPlaneKeyID),
		IsolationScopeID:   input.IsolationScopeID,
		APIKeyID:           apiKeyID,
		APIKeyName:         input.APIKeyName,
		GroupID:            cloneInt64Ptr(input.GroupID),
		GroupName:          input.GroupName,
		Endpoint:           input.Endpoint,
		Provider:           input.Provider,
		Model:              input.Model,
		Mode:               cfg.Mode,
		Action:             action,
		Flagged:            flagged,
		HighestCategory:    highestCategory,
		HighestScore:       highestScore,
		CategoryScores:     cloneFloatMap(scores),
		ThresholdSnapshot:  cloneFloatMap(cfg.Thresholds),
		InputExcerpt:       trimRunes(redactContentModerationSecrets(text), maxModerationExcerptRunes),
		UpstreamLatencyMS:  latency,
		QueueDelayMS:       queueDelay,
		Error:              errText,
	}
}

func applyContentPolicyLogEvidence(log *ContentModerationLog, input ContentModerationCheckInput, effectiveGroupID int64, effectiveGroupName, source string, languages []string, rerouted bool, reason string) {
	if log == nil {
		return
	}
	log.OriginalGroupID = cloneInt64Ptr(input.GroupID)
	log.OriginalGroupName = input.GroupName
	if effectiveGroupID > 0 {
		log.EffectiveGroupID = &effectiveGroupID
		log.GroupID = &effectiveGroupID
	}
	log.EffectiveGroupName = strings.TrimSpace(effectiveGroupName)
	if log.EffectiveGroupName != "" {
		log.GroupName = log.EffectiveGroupName
	}
	log.ContentSource = strings.TrimSpace(source)
	log.DetectedLanguages = append([]string(nil), languages...)
	log.KeywordPolicyRerouted = rerouted
	log.KeywordPolicyReason = strings.TrimSpace(reason)
}

func contentModerationInputSource(segments []ContentModerationSegment) string {
	var source string
	for _, segment := range segments {
		candidate := strings.TrimSpace(segment.Source)
		if candidate == "" {
			continue
		}
		if source == "" {
			source = candidate
			continue
		}
		if source != candidate {
			return "mixed"
		}
	}
	return source
}

func contentModerationUnsupportedLanguageSource(segments []ContentModerationSegment, unsupported []string) string {
	if len(segments) <= 1 || len(unsupported) == 0 {
		return contentModerationInputSource(segments)
	}
	unsupportedSet := make(map[string]struct{}, len(unsupported))
	for _, language := range unsupported {
		unsupportedSet[language] = struct{}{}
	}
	matched := make([]ContentModerationSegment, 0, len(segments))
	for _, segment := range segments {
		languages := detectContentLanguages(segment.Text)
		for _, language := range languages {
			if _, ok := unsupportedSet[language]; ok {
				matched = append(matched, segment)
				break
			}
		}
	}
	if source := contentModerationInputSource(matched); source != "" {
		return source
	}
	// 多个很短片段可能只有合并后才越过门槛；这种情况下保留总体来源，
	// 不能为了凑一个标签而把它错误归给 user（用户消息）。
	return contentModerationInputSource(segments)
}

func (s *ContentModerationService) persistContentModerationLog(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog, hashText string, recordHash bool, applySideEffects bool) {
	if s == nil || log == nil {
		return
	}
	// 指纹只服务于语种误判与客户端重试聚合。其他风控日志既不需要这份
	// 关联证据，也不应为每次持久化增加一次 HMAC 和索引写入。
	if log.ContentFingerprint == "" && log.Action == ContentModerationActionLanguageBlock {
		var userID, apiKeyID int64
		if log.UserID != nil {
			userID = *log.UserID
		}
		if log.APIKeyID != nil {
			apiKeyID = *log.APIKeyID
		}
		log.ContentFingerprint = s.contentModerationAuditFingerprintForSubject(
			log.OrganizationID, userID, apiKeyID, log.ControlPlaneKeyID, hashText,
		)
	}
	if recordHash && s.hashCache != nil {
		if err := s.hashCache.RecordFlaggedInputHash(ctx, hashText); err != nil {
			slog.Warn("content_moderation.record_hash_failed", "user_id", contentModerationEmailUserID(log), "endpoint", log.Endpoint, "error", err)
		}
	}
	autoBanJustApplied := false
	if applySideEffects {
		autoBanJustApplied = s.applyFlaggedAccountSideEffects(ctx, cfg, log)
		s.sendFlaggedNotificationSideEffects(ctx, cfg, log, autoBanJustApplied)
	}
	if s.repo != nil {
		if err := s.repo.CreateLog(ctx, log); err != nil {
			slog.Warn("content_moderation.create_log_failed", "user_id", contentModerationEmailUserID(log), "endpoint", log.Endpoint, "action", log.Action, "error", err)
			return
		}
	}
}

func (s *ContentModerationService) applyFlaggedAccountSideEffects(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog) bool {
	if s == nil || cfg == nil || log == nil || !log.Flagged || log.UserID == nil || *log.UserID <= 0 {
		return false
	}
	shouldApplyPenalty := contentModerationShouldApplyUserPenalty(log.Action)
	count := 1
	if s.repo != nil && cfg.ViolationWindowHours > 0 {
		since := time.Now().Add(-time.Duration(cfg.ViolationWindowHours) * time.Hour)
		scope := ContentModerationPenaltyScope{UserID: *log.UserID, OrganizationID: log.OrganizationID, ControlPlaneKeyID: log.ControlPlaneKeyID}
		if counter, ok := s.repo.(contentModerationSubjectCounter); ok {
			if n, err := counter.CountFlaggedBySubjectSince(ctx, scope, since, cfg.CyberPolicyExcludeFromBanCount); err == nil {
				count = n + 1
			}
		} else if scope.OrganizationID == 0 {
			if n, err := s.repo.CountFlaggedByUserSince(ctx, scope.UserID, since, cfg.CyberPolicyExcludeFromBanCount); err == nil {
				count = n + 1
			}
		}
	}
	log.ViolationCount = count
	autoBanJustApplied := false
	reportedToControlPlane := false
	remoteIdentity := strings.TrimSpace(log.ControlPlaneKeyID) != "" || log.OrganizationID > 0 ||
		strings.TrimSpace(log.BillingSubjectType) != "" || log.BillingSubjectID > 0
	if shouldApplyPenalty && remoteIdentity {
		reportedToControlPlane = s.reportContentModerationPenaltyToControlPlane(ctx, cfg, log)
	}
	if remoteIdentity {
		// 主站快照身份不属于本地 users 表。即使主站或本地数据库暂时不可用，
		// 也只能等待权威处罚重投，绝不能按碰巧相同的 user_id 扣本地余额或封个人账号。
		if shouldApplyPenalty && !reportedToControlPlane {
			slog.Warn("content_moderation.remote_penalty_deferred",
				"user_id", *log.UserID, "organization_id", log.OrganizationID,
				"request_id", log.RequestID, "action", log.Action)
		}
		return false
	}
	needsUser := s.userRepo != nil &&
		((cfg.AutoBanEnabled && cfg.BanThreshold > 0 && count >= cfg.BanThreshold) ||
			(!reportedToControlPlane && shouldApplyPenalty && cfg.DeductBalanceOnBlockEnabled && cfg.DeductBalanceOnBlockAmount > 0) ||
			(!reportedToControlPlane && shouldApplyPenalty && cfg.PermanentBanEnabled && cfg.PermanentBanThreshold > 0))
	var user *User
	if needsUser {
		var err error
		user, err = s.userRepo.GetByID(ctx, *log.UserID)
		if err != nil {
			slog.Warn("content_moderation.get_user_for_penalty_failed", "user_id", *log.UserID, "error", err)
			return false
		}
		if user.IsAdmin() {
			slog.Warn("content_moderation.user_penalty_skipped_admin", "user_id", *log.UserID, "role", user.Role, "action", log.Action, "count", count)
		}
	}
	if !reportedToControlPlane && shouldApplyPenalty && user != nil && !user.IsAdmin() {
		deductAmount := 0.0
		if cfg.DeductBalanceOnBlockEnabled && cfg.DeductBalanceOnBlockAmount > 0 {
			deductAmount = cfg.DeductBalanceOnBlockAmount
			if err := s.userRepo.DeductBalance(ctx, *log.UserID, deductAmount); err != nil {
				slog.Warn("content_moderation.deduct_balance_failed", "user_id", *log.UserID, "amount", deductAmount, "error", err)
				deductAmount = 0
			} else if s.authCacheInvalidator != nil {
				s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, *log.UserID)
			}
		}
		if s.repo != nil {
			penalty, err := s.repo.RecordUserPenalty(ctx, ContentModerationRecordUserPenaltyInput{
				UserID:                *log.UserID,
				DeductBalance:         deductAmount,
				PermanentBanEnabled:   cfg.PermanentBanEnabled,
				PermanentBanThreshold: cfg.PermanentBanThreshold,
				PermanentBanReason:    fmt.Sprintf("content moderation blocked %s %d times", log.Action, cfg.PermanentBanThreshold),
			})
			if err != nil {
				slog.Warn("content_moderation.record_user_penalty_failed", "user_id", *log.UserID, "action", log.Action, "error", err)
			} else if penalty != nil && penalty.PermanentlyBanned && user.Status != StatusDisabled {
				user.Status = StatusDisabled
				if err := s.userRepo.Update(ctx, user, UserUpdateFields{Status: true}); err != nil {
					slog.Warn("content_moderation.permanent_ban_update_user_failed", "user_id", *log.UserID, "error", err)
				} else {
					if s.authCacheInvalidator != nil {
						s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, *log.UserID)
					}
					autoBanJustApplied = true
				}
				log.AutoBanned = true
			}
		}
	}
	if cfg.AutoBanEnabled && cfg.BanThreshold > 0 && count >= cfg.BanThreshold && s.userRepo != nil {
		if user == nil {
			var err error
			user, err = s.userRepo.GetByID(ctx, *log.UserID)
			if err != nil {
				slog.Warn("content_moderation.ban_get_user_failed", "user_id", *log.UserID, "error", err)
				return false
			}
		}
		if user.IsAdmin() {
			slog.Warn("content_moderation.autoban_skipped_admin", "user_id", *log.UserID, "role", user.Role, "count", count, "threshold", cfg.BanThreshold)
			// TODO: Disable the triggering API key instead when API key mutation is available here.
			return false
		}
		if user.Status != StatusDisabled {
			user.Status = StatusDisabled
			if err := s.userRepo.Update(ctx, user, UserUpdateFields{Status: true}); err != nil {
				slog.Warn("content_moderation.ban_update_user_failed", "user_id", *log.UserID, "error", err)
				return false
			}
			if s.authCacheInvalidator != nil {
				s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, *log.UserID)
			}
			autoBanJustApplied = true
		}
		log.AutoBanned = true
	}
	return autoBanJustApplied
}

func (s *ContentModerationService) reportContentModerationPenaltyToControlPlane(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog) bool {
	reporter := gatewaylite.DefaultContentModerationPenaltyReporter()
	ref := gatewaylite.DefaultControlPlaneClientRef()
	if cfg == nil || log == nil || log.UserID == nil || *log.UserID <= 0 {
		return false
	}
	if reporter == nil && (ref == nil || !ref.Configured()) {
		return false
	}
	deductCents := int64(0)
	if cfg.DeductBalanceOnBlockEnabled && cfg.DeductBalanceOnBlockAmount > 0 {
		deductCents = int64(math.Round(cfg.DeductBalanceOnBlockAmount * 100))
		if deductCents < 0 {
			deductCents = 0
		}
	}
	meta, _ := gatewaylite.RequestMetaFromContext(ctx)
	requestID := strings.TrimSpace(log.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(meta.RequestID)
	}
	if requestID == "" {
		// 主站用 request_id 做幂等保护；没有 request_id 时不上报，避免同一命中在重试时重复扣款。
		slog.Warn("content_moderation.control_plane_penalty_skipped_missing_request_id", "user_id", *log.UserID, "action", log.Action)
		return false
	}
	keyID := strings.TrimSpace(meta.KeyID)
	if keyID == "" {
		keyID = strings.TrimSpace(log.ControlPlaneKeyID)
	}
	// APIKeyName 是运营可编辑的显示名，不是主站 Key 的稳定身份。远程请求
	// 缺少 control-plane key_id 时拒绝猜测，避免处罚落到错误付款主体。
	if (log.OrganizationID > 0 || strings.TrimSpace(log.BillingSubjectType) != "") && keyID == "" {
		slog.Warn("content_moderation.control_plane_penalty_skipped_missing_key_id",
			"user_id", *log.UserID, "organization_id", log.OrganizationID, "request_id", requestID)
		return false
	}
	region := strings.TrimSpace(meta.Region)
	gatewayID := strings.TrimSpace(meta.GatewayID)
	reason := fmt.Sprintf("content moderation blocked %s", log.Action)
	if cfg.PermanentBanEnabled && cfg.PermanentBanThreshold > 0 {
		reason = fmt.Sprintf("%s; permanent threshold=%d", reason, cfg.PermanentBanThreshold)
	}
	req := gatewaylite.ContentModerationPenaltyRequest{
		RequestID:             requestID,
		UserID:                *log.UserID,
		OrganizationID:        log.OrganizationID,
		BillingSubjectType:    strings.TrimSpace(log.BillingSubjectType),
		BillingSubjectID:      log.BillingSubjectID,
		KeyID:                 keyID,
		Region:                region,
		GatewayID:             gatewayID,
		Action:                log.Action,
		Category:              log.HighestCategory,
		DeductBalanceCents:    deductCents,
		PermanentBanEnabled:   cfg.PermanentBanEnabled,
		PermanentBanThreshold: cfg.PermanentBanThreshold,
		ViolationWindowHours:  cfg.ViolationWindowHours,
		Reason:                reason,
		CreatedAtMillis:       time.Now().UnixMilli(),
	}
	if log.OrganizationID > 0 {
		threshold := 0
		if cfg.AutoBanEnabled && cfg.BanThreshold > 0 {
			threshold = cfg.BanThreshold
		}
		if cfg.PermanentBanEnabled && cfg.PermanentBanThreshold > 0 &&
			(threshold == 0 || cfg.PermanentBanThreshold < threshold) {
			threshold = cfg.PermanentBanThreshold
		}
		req.OrganizationSuspensionEnabled = threshold > 0
		req.OrganizationSuspensionThreshold = threshold
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	if reporter != nil {
		err = reporter.Report(reportCtx, req)
	} else {
		_, err = ref.ReportContentModerationPenalty(reportCtx, req)
	}
	if err != nil {
		slog.Warn("content_moderation.control_plane_penalty_report_failed", "user_id", req.UserID, "request_id", req.RequestID, "action", req.Action, "error", err)
		return false
	}
	return true
}

func (s *ContentModerationService) sendFlaggedNotificationSideEffects(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog, autoBanJustApplied bool) {
	if s == nil || cfg == nil || log == nil || !log.Flagged {
		return
	}
	if s.emailService == nil || strings.TrimSpace(log.UserEmail) == "" {
		return
	}
	emailSent := false
	if cfg.EmailOnHit {
		if err := s.sendViolationEmail(ctx, cfg, log); err != nil {
			slog.Warn("content_moderation.email_failed", "user_id", *log.UserID, "email", log.UserEmail, "error", err)
		} else {
			emailSent = true
		}
	}
	if autoBanJustApplied {
		if err := s.sendAccountDisabledEmail(ctx, cfg, log); err != nil {
			slog.Warn("content_moderation.ban_email_failed", "user_id", *log.UserID, "email", log.UserEmail, "error", err)
		} else {
			emailSent = true
		}
	}
	log.EmailSent = emailSent
}

func contentModerationShouldApplyUserPenalty(action string) bool {
	switch action {
	case ContentModerationActionBlock, ContentModerationActionKeywordBlock, ContentModerationActionCyberPolicy:
		return true
	default:
		return false
	}
}

func (s *ContentModerationService) sendViolationEmail(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog) error {
	siteName := s.siteName(ctx)
	if s.emailService.notificationEmailService != nil {
		if err := s.emailService.notificationEmailService.Send(ctx, NotificationEmailSendInput{
			Event:          NotificationEmailEventContentModerationViolation,
			RecipientEmail: log.UserEmail,
			RecipientName:  emailRecipientName(log.UserEmail),
			UserID:         contentModerationEmailUserID(log),
			SourceType:     "content_moderation",
			SourceID:       contentModerationEmailSourceID(log),
			Variables:      contentModerationEmailVariables(log, cfg),
		}); err == nil {
			return nil
		} else {
			if !shouldFallbackNotificationEmail(err) {
				return err
			}
			slog.Warn("template content moderation violation email failed; falling back to built-in body", "log_id", log.ID, "recipient_hash", notificationEmailHash(log.UserEmail), "err", err.Error())
		}
	}
	subject := fmt.Sprintf("[%s] 账户风控提醒 / Risk Control Notice", sanitizeEmailHeader(siteName))
	body := buildContentModerationViolationEmailBody(siteName, log, cfg)
	return s.emailService.SendEmail(ctx, log.UserEmail, subject, body)
}

func (s *ContentModerationService) sendAccountDisabledEmail(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog) error {
	siteName := s.siteName(ctx)
	if s.emailService.notificationEmailService != nil {
		if err := s.emailService.notificationEmailService.Send(ctx, NotificationEmailSendInput{
			Event:          NotificationEmailEventContentModerationDisabled,
			RecipientEmail: log.UserEmail,
			RecipientName:  emailRecipientName(log.UserEmail),
			UserID:         contentModerationEmailUserID(log),
			SourceType:     "content_moderation",
			SourceID:       contentModerationEmailSourceID(log),
			Variables:      contentModerationEmailVariables(log, cfg),
		}); err == nil {
			return nil
		} else {
			if !shouldFallbackNotificationEmail(err) {
				return err
			}
			slog.Warn("template content moderation disabled email failed; falling back to built-in body", "log_id", log.ID, "recipient_hash", notificationEmailHash(log.UserEmail), "err", err.Error())
		}
	}
	subject := fmt.Sprintf("[%s] 账户已被禁用 / Account Disabled", sanitizeEmailHeader(siteName))
	body := buildContentModerationAccountDisabledEmailBody(siteName, log, cfg)
	return s.emailService.SendEmail(ctx, log.UserEmail, subject, body)
}

func contentModerationEmailUserID(log *ContentModerationLog) int64 {
	if log == nil || log.UserID == nil {
		return 0
	}
	return *log.UserID
}

func contentModerationEmailSourceID(log *ContentModerationLog) string {
	if log == nil || log.ID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", log.ID)
}

func contentModerationEmailVariables(log *ContentModerationLog, cfg *ContentModerationConfig) map[string]string {
	variables := map[string]string{
		"triggered_at":        time.Now().UTC().Format(time.RFC3339),
		"group_name":          "-",
		"moderation_category": "-",
		"moderation_score":    "0.000",
		"violation_count":     "0",
		"ban_threshold":       "0",
	}
	if log != nil {
		if !log.CreatedAt.IsZero() {
			variables["triggered_at"] = log.CreatedAt.UTC().Format(time.RFC3339)
		}
		if strings.TrimSpace(log.GroupName) != "" {
			variables["group_name"] = strings.TrimSpace(log.GroupName)
		}
		if strings.TrimSpace(log.HighestCategory) != "" {
			variables["moderation_category"] = strings.TrimSpace(log.HighestCategory)
		}
		variables["moderation_score"] = fmt.Sprintf("%.3f", log.HighestScore)
		variables["violation_count"] = fmt.Sprintf("%d", log.ViolationCount)
	}
	if cfg != nil {
		variables["ban_threshold"] = fmt.Sprintf("%d", cfg.BanThreshold)
	}
	return variables
}

func (s *ContentModerationService) siteName(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return "tokenav gateway"
	}
	name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || strings.TrimSpace(name) == "" {
		return "tokenav gateway"
	}
	return strings.TrimSpace(name)
}

func defaultContentModerationConfig() *ContentModerationConfig {
	return &ContentModerationConfig{
		AI:                          defaultContentModerationAIConfig(),
		Enabled:                     false,
		Mode:                        ContentModerationModePreBlock,
		BaseURL:                     defaultContentModerationBaseURL,
		Model:                       defaultContentModerationModel,
		TimeoutMS:                   defaultContentModerationTimeoutMS,
		SampleRate:                  100,
		AllGroups:                   true,
		GroupIDs:                    []int64{},
		RecordNonHits:               false,
		Thresholds:                  ContentModerationDefaultThresholds(),
		WorkerCount:                 defaultContentModerationWorkerCount,
		QueueSize:                   defaultContentModerationQueueSize,
		BlockStatus:                 defaultContentModerationBlockHTTPStatus,
		BlockMessage:                defaultContentModerationBlockMessage,
		EmailOnHit:                  true,
		AutoBanEnabled:              false,
		BanThreshold:                defaultContentModerationBanThreshold,
		DeductBalanceOnBlockEnabled: false,
		DeductBalanceOnBlockAmount:  0,
		PermanentBanEnabled:         false,
		PermanentBanThreshold:       defaultContentModerationPermanentBanThreshold,
		ViolationWindowHours:        defaultContentModerationViolationWindowHours,
		RetryCount:                  defaultContentModerationRetryCount,
		HitRetentionDays:            defaultContentModerationHitRetentionDays,
		NonHitRetentionDays:         defaultContentModerationNonHitRetentionDays,
		PreHashCheckEnabled:         false,
		BlockedKeywords:             []string{},
		KeywordBlockingMode:         ContentModerationKeywordModeKeywordOnly,
		KeywordRulePacks:            defaultContentModerationKeywordRulePacks(),
		PolicyVersion:               2,
		LocalKeywordOnly:            true,
		AllowedLanguages:            []string{"zh", "en"},
		ScanLatestUserInput:         true,
		ScanNewSkillContent:         true,
		ScanNewToolOutput:           true,
		ScanTopLevelInstructions:    true,
		KeywordGroupRoutingEnabled:  true,
		ModelFilter: ContentModerationModelFilter{
			Type:   ContentModerationModelFilterAll,
			Models: []string{},
		},
		CyberPolicyExcludeFromBanCount: false,
	}
}

func cloneContentModerationConfig(cfg *ContentModerationConfig) *ContentModerationConfig {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.APIKeys = append([]string(nil), cfg.APIKeys...)
	clone.GroupIDs = append([]int64(nil), cfg.GroupIDs...)
	clone.BlockedKeywords = append([]string(nil), cfg.BlockedKeywords...)
	clone.KeywordRulePacks = cloneContentModerationKeywordRulePacks(cfg.KeywordRulePacks)
	clone.KeywordRulePackOverrides = cloneContentModerationKeywordRulePackOverrides(cfg.KeywordRulePackOverrides)
	clone.AllowedLanguages = append([]string(nil), cfg.AllowedLanguages...)
	clone.Thresholds = cloneFloatMap(cfg.Thresholds)
	clone.ModelFilter = ContentModerationModelFilter{
		Type:   cfg.ModelFilter.Type,
		Models: append([]string(nil), cfg.ModelFilter.Models...),
	}
	return &clone
}

func (cfg *ContentModerationConfig) normalize() {
	cfg.AI.normalize()
	aiOnly := cfg.KeywordBlockingMode == ContentModerationKeywordModeAIOnly
	if cfg.PolicyVersion <= 0 {
		// 旧配置没有这些字段；升级时采用安全且不重复扫描历史的默认值。
		cfg.ScanLatestUserInput = true
		cfg.ScanNewSkillContent = true
		cfg.ScanNewToolOutput = true
		cfg.KeywordGroupRoutingEnabled = true
		cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
		cfg.PolicyVersion = 1
	}
	if cfg.PolicyVersion < 2 {
		// v2 才加入顶层 instructions 开关。旧 JSON 缺字段会反序列化为 false，
		// 必须在版本迁移中显式打开，避免升级后继续保持漏检。
		cfg.ScanTopLevelInstructions = true
		cfg.PolicyVersion = 2
	}
	if aiOnly {
		cfg.KeywordBlockingMode = ContentModerationKeywordModeAIOnly
		cfg.SampleRate = 100
	}
	cfg.KeywordBlockingMode = normalizeKeywordBlockingMode(cfg.KeywordBlockingMode)
	// 正常管理界面只保存 keyword_only；这里保留旧模式的底层解析，仅用于
	// 旧数据迁移与独立兼容测试。产品热路径是否允许远程调用由此只读事实决定。
	cfg.LocalKeywordOnly = cfg.KeywordBlockingMode == ContentModerationKeywordModeKeywordOnly
	cfg.AllowedLanguages = normalizeAllowedLanguages(cfg.AllowedLanguages)
	if cfg.APIKey != "" {
		cfg.APIKeys = normalizeModerationAPIKeys(append(cfg.APIKeys, cfg.APIKey))
		cfg.APIKey = ""
	} else {
		cfg.APIKeys = normalizeModerationAPIKeys(cfg.APIKeys)
	}
	if cfg.Mode == "" {
		cfg.Mode = ContentModerationModePreBlock
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultContentModerationBaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.Model == "" {
		cfg.Model = defaultContentModerationModel
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = defaultContentModerationTimeoutMS
	}
	if cfg.TimeoutMS > maxContentModerationTimeoutMS {
		cfg.TimeoutMS = maxContentModerationTimeoutMS
	}
	if cfg.SampleRate < 0 {
		cfg.SampleRate = 0
	}
	if cfg.SampleRate > 100 {
		cfg.SampleRate = 100
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = defaultContentModerationWorkerCount
	}
	if cfg.WorkerCount > maxContentModerationWorkerCount {
		cfg.WorkerCount = maxContentModerationWorkerCount
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultContentModerationQueueSize
	}
	if cfg.QueueSize > maxContentModerationQueueSize {
		cfg.QueueSize = maxContentModerationQueueSize
	}
	if strings.TrimSpace(cfg.BlockMessage) == "" {
		cfg.BlockMessage = defaultContentModerationBlockMessage
	}
	cfg.BlockMessage = strings.TrimSpace(cfg.BlockMessage)
	if cfg.BlockStatus <= 0 {
		cfg.BlockStatus = defaultContentModerationBlockHTTPStatus
	}
	if cfg.BanThreshold <= 0 {
		cfg.BanThreshold = defaultContentModerationBanThreshold
	}
	if cfg.DeductBalanceOnBlockAmount < 0 {
		cfg.DeductBalanceOnBlockAmount = 0
	}
	if cfg.PermanentBanThreshold <= 0 {
		cfg.PermanentBanThreshold = defaultContentModerationPermanentBanThreshold
	}
	if cfg.ViolationWindowHours <= 0 {
		cfg.ViolationWindowHours = defaultContentModerationViolationWindowHours
	}
	if cfg.RetryCount < 0 {
		cfg.RetryCount = 0
	}
	if cfg.RetryCount > maxContentModerationRetryCount {
		cfg.RetryCount = maxContentModerationRetryCount
	}
	if cfg.HitRetentionDays <= 0 {
		cfg.HitRetentionDays = defaultContentModerationHitRetentionDays
	}
	if cfg.HitRetentionDays > maxContentModerationRetentionDays {
		cfg.HitRetentionDays = maxContentModerationRetentionDays
	}
	if cfg.NonHitRetentionDays <= 0 {
		cfg.NonHitRetentionDays = defaultContentModerationNonHitRetentionDays
	}
	if cfg.NonHitRetentionDays > maxContentModerationNonHitRetentionDays {
		cfg.NonHitRetentionDays = maxContentModerationNonHitRetentionDays
	}
	cfg.GroupIDs = normalizeInt64IDs(cfg.GroupIDs)
	cfg.Thresholds = mergeContentModerationThresholds(ContentModerationDefaultThresholds(), cfg.Thresholds)
	cfg.BlockedKeywords = normalizeBlockedKeywords(cfg.BlockedKeywords)
	cfg.KeywordBlockingMode = normalizeKeywordBlockingMode(cfg.KeywordBlockingMode)
	cfg.KeywordRulePacks = normalizeContentModerationKeywordRulePacks(cfg.KeywordRulePacks)
	cfg.KeywordRulePackOverrides = normalizeContentModerationKeywordRulePackOverrides(cfg.KeywordRulePackOverrides)
	cfg.ModelFilter = normalizeContentModerationModelFilter(cfg.ModelFilter)
}

func (cfg *ContentModerationConfig) includesGroup(groupID *int64) bool {
	if cfg.AllGroups {
		return true
	}
	if groupID == nil {
		return false
	}
	for _, id := range cfg.GroupIDs {
		if id == *groupID {
			return true
		}
	}
	return false
}

func (cfg *ContentModerationConfig) includesModel(model string) bool {
	if cfg == nil {
		return true
	}
	filter := normalizeContentModerationModelFilter(cfg.ModelFilter)
	switch filter.Type {
	case ContentModerationModelFilterInclude:
		return contentModerationModelListContains(filter.Models, model)
	case ContentModerationModelFilterExclude:
		return !contentModerationModelListContains(filter.Models, model)
	default:
		return true
	}
}

func contentModerationLogGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}

func (cfg *ContentModerationConfig) shouldSample(hashText string) bool {
	if cfg.SampleRate >= 100 {
		return true
	}
	if cfg.SampleRate <= 0 {
		return false
	}
	raw, err := hex.DecodeString(hashText)
	if err != nil || len(raw) < 2 {
		return true
	}
	return int(binary.BigEndian.Uint16(raw[:2])%100) < cfg.SampleRate
}

func (cfg *ContentModerationConfig) apiKeys() []string {
	if cfg == nil {
		return nil
	}
	return normalizeModerationAPIKeys(cfg.APIKeys)
}

func (s *ContentModerationService) nextUsableAPIKey(cfg *ContentModerationConfig) (string, bool) {
	keys := cfg.apiKeys()
	if len(keys) == 0 {
		return "", false
	}
	now := time.Now()
	for i := 0; i < len(keys); i++ {
		idx := int(s.apiKeyCursor.Add(1)-1) % len(keys)
		key := keys[idx]
		if !s.isAPIKeyFrozen(key, now) {
			return key, true
		}
	}
	return "", false
}

func (s *ContentModerationService) isAPIKeyFrozen(key string, now time.Time) bool {
	hash := moderationAPIKeyHash(key)
	if hash == "" || s == nil {
		return false
	}
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.keyHealth[hash]
	return state != nil && state.FrozenUntil.After(now)
}

func (s *ContentModerationService) beginModerationAPIKeyCall(key string) {
	hash := moderationAPIKeyHash(key)
	if hash == "" || s == nil {
		return
	}
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.ensureAPIKeyHealthLocked(hash, maskSecretTail(key))
	state.SyncActive++
}

func (s *ContentModerationService) finishModerationAPIKeyCall(key string, latencyMS int, success bool) {
	hash := moderationAPIKeyHash(key)
	if hash == "" || s == nil {
		return
	}
	if latencyMS < 0 {
		latencyMS = 0
	}
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.ensureAPIKeyHealthLocked(hash, maskSecretTail(key))
	if state.SyncActive > 0 {
		state.SyncActive--
	}
	state.SyncTotal++
	state.SyncLatencyMS += int64(latencyMS)
	if success {
		state.SyncSuccess++
		return
	}
	state.SyncErrors++
}

func (s *ContentModerationService) markAPIKeySuccess(key string, latencyMS int, httpStatus int) {
	hash := moderationAPIKeyHash(key)
	if hash == "" || s == nil {
		return
	}
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.ensureAPIKeyHealthLocked(hash, maskSecretTail(key))
	state.FailureCount = 0
	state.SuccessCount++
	state.LastError = ""
	state.LastCheckedAt = time.Now()
	state.FrozenUntil = time.Time{}
	state.LastLatencyMS = latencyMS
	state.LastHTTPStatus = httpStatus
	state.LastTested = true
}

func (s *ContentModerationService) markAPIKeyError(key string, errText string, latencyMS int, httpStatus int) {
	hash := moderationAPIKeyHash(key)
	if hash == "" || s == nil {
		return
	}
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.ensureAPIKeyHealthLocked(hash, maskSecretTail(key))
	if contentModerationFreezeDurationForHTTPStatus(httpStatus) > 0 {
		state.FailureCount++
	}
	state.LastError = trimRunes(errText, 180)
	state.LastCheckedAt = time.Now()
	state.LastLatencyMS = latencyMS
	state.LastHTTPStatus = httpStatus
	state.LastTested = true
	if freezeDuration := contentModerationFreezeDurationForHTTPStatus(httpStatus); freezeDuration > 0 {
		state.FrozenUntil = time.Now().Add(freezeDuration)
	}
}

func contentModerationFreezeDurationForHTTPStatus(httpStatus int) time.Duration {
	switch httpStatus {
	case 0, http.StatusBadRequest:
		return 0
	case http.StatusUnauthorized, http.StatusForbidden:
		return contentModerationKeyAuthFreezeDuration
	case http.StatusTooManyRequests, 529:
		return contentModerationKeyRateLimitFreezeDuration
	default:
		return contentModerationKeyHTTPErrorFreezeDuration
	}
}

func (s *ContentModerationService) ensureAPIKeyHealthLocked(hash string, masked string) *contentModerationKeyHealth {
	if s.keyHealth == nil {
		s.keyHealth = make(map[string]*contentModerationKeyHealth)
	}
	state := s.keyHealth[hash]
	if state == nil {
		state = &contentModerationKeyHealth{Hash: hash}
		s.keyHealth[hash] = state
	}
	if strings.TrimSpace(masked) != "" {
		state.Masked = masked
	}
	return state
}

func (s *ContentModerationService) configView(cfg *ContentModerationConfig) *ContentModerationConfigView {
	keys := cfg.apiKeys()
	masks := make([]string, 0, len(keys))
	for _, key := range keys {
		masks = append(masks, maskSecretTail(key))
	}
	apiKeyMasked := ""
	if len(masks) > 0 {
		apiKeyMasked = masks[0]
	}
	return &ContentModerationConfigView{
		AI:                             s.contentModerationAIConfigView(cfg),
		Enabled:                        cfg.Enabled,
		Mode:                           cfg.Mode,
		BaseURL:                        cfg.BaseURL,
		Model:                          cfg.Model,
		APIKeyConfigured:               len(keys) > 0,
		APIKeyMasked:                   apiKeyMasked,
		APIKeyCount:                    len(keys),
		APIKeyMasks:                    masks,
		APIKeyStatuses:                 s.apiKeyStatuses(keys),
		TimeoutMS:                      cfg.TimeoutMS,
		SampleRate:                     cfg.SampleRate,
		AllGroups:                      cfg.AllGroups,
		GroupIDs:                       append([]int64(nil), cfg.GroupIDs...),
		RecordNonHits:                  cfg.RecordNonHits,
		Thresholds:                     cloneFloatMap(cfg.Thresholds),
		WorkerCount:                    cfg.WorkerCount,
		QueueSize:                      cfg.QueueSize,
		BlockStatus:                    cfg.BlockStatus,
		BlockMessage:                   cfg.BlockMessage,
		EmailOnHit:                     cfg.EmailOnHit,
		AutoBanEnabled:                 cfg.AutoBanEnabled,
		BanThreshold:                   cfg.BanThreshold,
		DeductBalanceOnBlockEnabled:    cfg.DeductBalanceOnBlockEnabled,
		DeductBalanceOnBlockAmount:     cfg.DeductBalanceOnBlockAmount,
		PermanentBanEnabled:            cfg.PermanentBanEnabled,
		PermanentBanThreshold:          cfg.PermanentBanThreshold,
		ViolationWindowHours:           cfg.ViolationWindowHours,
		RetryCount:                     cfg.RetryCount,
		HitRetentionDays:               cfg.HitRetentionDays,
		NonHitRetentionDays:            cfg.NonHitRetentionDays,
		PreHashCheckEnabled:            cfg.PreHashCheckEnabled,
		BlockedKeywords:                append([]string(nil), cfg.BlockedKeywords...),
		KeywordBlockingMode:            cfg.KeywordBlockingMode,
		KeywordRulePacks:               cloneContentModerationKeywordRulePacks(cfg.KeywordRulePacks),
		ModelFilter:                    cloneContentModerationModelFilter(cfg.ModelFilter),
		PolicyVersion:                  cfg.PolicyVersion,
		LocalKeywordOnly:               cfg.LocalKeywordOnly,
		AllowedLanguages:               append([]string(nil), cfg.AllowedLanguages...),
		ScanLatestUserInput:            cfg.ScanLatestUserInput,
		ScanNewSkillContent:            cfg.ScanNewSkillContent,
		ScanNewToolOutput:              cfg.ScanNewToolOutput,
		ScanTopLevelInstructions:       cfg.ScanTopLevelInstructions,
		KeywordGroupRoutingEnabled:     cfg.KeywordGroupRoutingEnabled,
		CyberPolicyExcludeFromBanCount: cfg.CyberPolicyExcludeFromBanCount,
	}
}

func (s *ContentModerationService) apiKeyStatuses(keys []string) []ContentModerationAPIKeyStatus {
	out := make([]ContentModerationAPIKeyStatus, 0, len(keys))
	for idx, key := range keys {
		out = append(out, s.apiKeyStatusForHash(idx, moderationAPIKeyHash(key), maskSecretTail(key), true))
	}
	return out
}

func (s *ContentModerationService) preBlockAPIKeyLoads(keys []string) []ContentModerationAPIKeyLoad {
	out := make([]ContentModerationAPIKeyLoad, 0, len(keys))
	for idx, key := range keys {
		out = append(out, s.preBlockAPIKeyLoadForHash(idx, moderationAPIKeyHash(key), maskSecretTail(key)))
	}
	return out
}

func (s *ContentModerationService) preBlockAPIKeyActive(keys []string) int64 {
	var total int64
	for _, item := range s.preBlockAPIKeyLoads(keys) {
		total += item.Active
	}
	return total
}

func (s *ContentModerationService) preBlockAPIKeyAvailableCount(keys []string) int64 {
	now := time.Now()
	var count int64
	for _, key := range keys {
		if !s.isAPIKeyFrozen(key, now) {
			count++
		}
	}
	return count
}

func (s *ContentModerationService) preBlockAPIKeyTotalCalls(keys []string) int64 {
	var total int64
	for _, item := range s.preBlockAPIKeyLoads(keys) {
		total += item.Total
	}
	return total
}

func (s *ContentModerationService) preBlockAPIKeyLoadForHash(index int, hash string, masked string) ContentModerationAPIKeyLoad {
	load := ContentModerationAPIKeyLoad{
		Index:   index,
		KeyHash: hash,
		Masked:  masked,
		Status:  "unknown",
	}
	status := s.apiKeyStatusForHash(index, hash, masked, true)
	load.Status = status.Status
	load.LastLatencyMS = status.LastLatencyMS
	load.LastHTTPStatus = status.LastHTTPStatus
	if hash == "" || s == nil {
		return load
	}
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.keyHealth[hash]
	if state == nil {
		return load
	}
	load.Active = state.SyncActive
	load.Total = state.SyncTotal
	load.Success = state.SyncSuccess
	load.Errors = state.SyncErrors
	if state.SyncTotal > 0 {
		load.AvgLatencyMS = state.SyncLatencyMS / state.SyncTotal
	}
	return load
}

func (s *ContentModerationService) apiKeyStatusForHash(index int, hash string, masked string, configured bool) ContentModerationAPIKeyStatus {
	status := ContentModerationAPIKeyStatus{
		Index:      index,
		KeyHash:    hash,
		Masked:     masked,
		Status:     "unknown",
		Configured: configured,
	}
	if hash == "" || s == nil {
		return status
	}
	now := time.Now()
	s.keyHealthMu.Lock()
	defer s.keyHealthMu.Unlock()
	state := s.keyHealth[hash]
	if state == nil {
		return status
	}
	status.FailureCount = state.FailureCount
	status.SuccessCount = state.SuccessCount
	status.LastError = state.LastError
	status.LastLatencyMS = state.LastLatencyMS
	status.LastHTTPStatus = state.LastHTTPStatus
	status.LastTested = state.LastTested
	if !state.LastCheckedAt.IsZero() {
		t := state.LastCheckedAt
		status.LastCheckedAt = &t
	}
	if state.FrozenUntil.After(now) {
		t := state.FrozenUntil
		status.FrozenUntil = &t
		status.Status = "frozen"
		return status
	}
	if state.LastError != "" {
		status.Status = "error"
		return status
	}
	if state.SuccessCount > 0 || state.LastTested {
		status.Status = "ok"
	}
	return status
}

func moderationAPIKeyHash(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func buildModerationTestInput(prompt string, images []string) (any, int, error) {
	prompt = trimRunes(normalizeContentModerationText(prompt), maxModerationInputRunes)
	normalizedImages := make([]string, 0, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if len(normalizedImages) >= maxContentModerationTestImages {
			return nil, 0, infraerrors.BadRequest("TOO_MANY_MODERATION_TEST_IMAGES", fmt.Sprintf("最多上传 %d 张测试图片", maxContentModerationTestImages))
		}
		if err := validateModerationTestImageDataURL(image); err != nil {
			return nil, 0, err
		}
		normalizedImages = append(normalizedImages, image)
	}
	if prompt == "" && len(normalizedImages) == 0 {
		return "hello", 0, nil
	}
	if len(normalizedImages) == 0 {
		return prompt, 0, nil
	}
	parts := make([]moderationAPIInputPart, 0, len(normalizedImages)+1)
	if prompt != "" {
		parts = append(parts, moderationAPIInputPart{Type: "text", Text: prompt})
	}
	for _, image := range normalizedImages {
		parts = append(parts, moderationAPIInputPart{
			Type:     "image_url",
			ImageURL: &moderationAPIImageURLRef{URL: image},
		})
	}
	return parts, len(normalizedImages), nil
}

func contentModerationTestHasAuditInput(prompt string, images []string) bool {
	if normalizeContentModerationText(prompt) != "" {
		return true
	}
	for _, image := range images {
		if strings.TrimSpace(image) != "" {
			return true
		}
	}
	return false
}

func validateModerationTestImageDataURL(value string) error {
	if len(value) > maxContentModerationTestImageDataURLBytes {
		return infraerrors.BadRequest("MODERATION_TEST_IMAGE_TOO_LARGE", "测试图片不能超过 8MB")
	}
	if !strings.HasPrefix(value, "data:image/") {
		return infraerrors.BadRequest("INVALID_MODERATION_TEST_IMAGE", "测试图片必须是 data:image/* base64")
	}
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 || !strings.Contains(parts[0], ";base64") {
		return infraerrors.BadRequest("INVALID_MODERATION_TEST_IMAGE", "测试图片必须是 base64 data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return infraerrors.BadRequest("INVALID_MODERATION_TEST_IMAGE", "测试图片 base64 无效")
	}
	if len(raw) > maxContentModerationTestImageBytes {
		return infraerrors.BadRequest("MODERATION_TEST_IMAGE_TOO_LARGE", "测试图片不能超过 8MB")
	}
	return nil
}

func buildContentModerationTestAuditResult(result *moderationAPIResult, thresholds map[string]float64) *ContentModerationTestAuditResult {
	if result == nil {
		return nil
	}
	scores := make(map[string]float64, len(result.CategoryScores))
	for category, score := range result.CategoryScores {
		scores[category] = score
	}
	thresholdSnapshot := mergeContentModerationThresholds(ContentModerationDefaultThresholds(), thresholds)
	flagged, highestCategory, highestScore := evaluateModerationScores(scores, thresholdSnapshot)
	compositeScore := highestScore
	return &ContentModerationTestAuditResult{
		Flagged:         flagged,
		HighestCategory: highestCategory,
		HighestScore:    highestScore,
		CompositeScore:  compositeScore,
		CategoryScores:  scores,
		Thresholds:      thresholdSnapshot,
	}
}

type moderationAPIRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

type moderationAPIInputPart struct {
	Type     string                    `json:"type"`
	Text     string                    `json:"text,omitempty"`
	ImageURL *moderationAPIImageURLRef `json:"image_url,omitempty"`
}

type moderationAPIImageURLRef struct {
	URL string `json:"url"`
}

type moderationAPIResponse struct {
	Results []moderationAPIResult `json:"results"`
}

type moderationAPIResult struct {
	Flagged        bool               `json:"flagged"`
	CategoryScores map[string]float64 `json:"category_scores"`
}

func evaluateModerationScores(scores map[string]float64, thresholds map[string]float64) (bool, string, float64) {
	flagged := false
	highestCategory := ""
	highestScore := 0.0
	for _, category := range contentModerationCategoryOrder {
		score := scores[category]
		if score > highestScore || highestCategory == "" {
			highestScore = score
			highestCategory = category
		}
		if score >= thresholds[category] {
			flagged = true
		}
	}
	for category, score := range scores {
		if score > highestScore || highestCategory == "" {
			highestScore = score
			highestCategory = category
		}
	}
	return flagged, highestCategory, highestScore
}

func mergeContentModerationThresholds(base map[string]float64, override map[string]float64) map[string]float64 {
	out := cloneFloatMap(base)
	if out == nil {
		out = map[string]float64{}
	}
	for _, category := range contentModerationCategoryOrder {
		if v, ok := override[category]; ok {
			if v < 0 {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			out[category] = v
		}
	}
	return out
}

func normalizeInt64IDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func normalizeBlockedKeywords(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		kw := strings.TrimSpace(raw)
		if kw == "" {
			continue
		}
		kw = trimRunes(kw, maxContentModerationBlockedKeywordRunes)
		key := strings.ToLower(kw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, kw)
		if len(out) >= maxContentModerationBlockedKeywords {
			break
		}
	}
	return out
}

func defaultContentModerationKeywordRulePacks() []ContentModerationKeywordRulePack {
	packs := []ContentModerationKeywordRulePack{
		{
			ID:          "credential_theft",
			Name:        "账号与密钥盗取",
			Description: "拦截索要、窃取或绕过 API Key、Cookie、Token、SSH 私钥等凭据的请求。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"盗key", "偷key", "窃取key", "盗取key", "泄露key", "倒卖key", "api key盗取", "api key泄露",
				"盗号", "偷号", "撞库", "拖库", "洗库", "扫号", "爆破账号", "破解账号", "账号密码库",
				"偷cookie", "盗cookie", "窃取cookie", "导出cookie", "cookie劫持", "session劫持",
				"盗token", "偷token", "窃取token", "导出token", "jwt绕过", "jwt破解",
				"私钥泄露", "导出私钥", "ssh私钥", "BEGIN RSA PRIVATE KEY", "BEGIN OPENSSH PRIVATE KEY",
				"steal api key", "leak api key", "steal cookie", "dump cookies", "steal token",
				"dump token", "session hijack", "jwt bypass", "private key leak",
			},
		},
		{
			ID:          "illegal_crime",
			Name:        "违法犯罪操作",
			Description: "拦截诈骗、洗钱、伪造证件、毒品、爆炸物、武器制作等明确违法操作。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"诈骗话术", "杀猪盘", "跑分", "跑分平台", "洗钱", "洗钱教程", "代收黑钱", "黑灰产",
				"银行卡四件套", "手机卡四件套", "对公账户买卖", "买卖银行卡", "盗刷", "盗刷信用卡",
				"信用卡盗刷", "cvv料", "黑卡", "假证", "假身份证", "伪造身份证", "伪造护照",
				"制作假币", "毒品交易", "制毒", "制毒教程", "炸药", "炸药配方", "炸弹制作",
				"爆炸物制作", "枪支制作", "买卖个人信息", "贩卖数据", "fake passport",
				"money laundering", "carding", "credit card fraud", "fake id", "make explosives",
			},
		},
		{
			ID:          "malware_abuse",
			Name:        "网络攻击与恶意代码",
			Description: "拦截免杀、后门、撞库、勒索、钓鱼、短信轰炸等可直接造成滥用的请求。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"免杀", "免杀马", "免杀木马", "绕杀软", "绕过杀软", "绕过edr", "绕过av",
				"后门", "后门程序", "持久化后门", "木马", "远控", "远控木马", "勒索软件",
				"勒索源码", "挖矿木马", "肉鸡", "僵尸网络", "c2控制端", "c2 server",
				"撞库脚本", "批量撞库", "爆破脚本", "弱口令爆破", "短信轰炸", "邮箱轰炸",
				"钓鱼页面", "钓鱼模板", "钓鱼链接", "盗号脚本", "抓密码", "抓取密码",
				"webshell", "一句话木马", "bypass antivirus", "bypass edr",
				"stealer malware", "ransomware", "phishing kit", "credential stuffing",
			},
		},
		{
			ID:          "minor_sexual_exploitation",
			Name:        "未成年人和性剥削",
			Description: "拦截未成年人性内容、非自愿色情、性交易、偷拍和裸照勒索等内容。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"儿童色情", "未成年色情", "未成年裸照", "幼女裸照", "萝莉色情", "幼态色情",
				"未成年约炮", "未成年性交易", "童模裸照", "csam", "child sexual", "minor nude",
				"child nude", "underage sex", "偷拍视频", "偷拍裙底", "偷拍厕所", "偷拍更衣室",
				"偷拍裸照", "裸照勒索", "艳照勒索", "迷奸", "迷奸教程", "迷药", "下药迷奸",
				"性交易联系方式", "援交资源", "约炮资源", "revenge porn", "non consensual nude",
				"non-consensual nude", "upskirt",
			},
		},
		{
			ID:          "self_harm_instructions",
			Name:        "自残具体方法",
			Description: "拦截具体自杀、自残方法、剂量和步骤；普通求助内容应由上游审计或模型安全回答处理。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"自杀方法", "自杀教程", "自杀步骤", "轻生方法", "怎么自杀", "怎么轻生",
				"怎么死不痛苦", "无痛自杀", "上吊教程", "割腕教程", "割腕位置", "跳楼不痛苦",
				"安眠药剂量", "过量服药", "自残教程", "自残方法", "自残步骤",
				"suicide method", "suicide guide", "painless suicide", "how to kill myself",
				"self harm instructions", "self-harm guide", "overdose dosage",
			},
		},
		{
			ID:          "privacy_doxxing",
			Name:        "隐私泄露和人肉",
			Description: "拦截查户籍、社工库、人肉、定位手机号、查询他人住址等隐私侵害请求。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"社工库", "社工库查询", "查户籍", "查开房", "查档", "查人信息", "查别人信息",
				"身份证反查", "手机号反查", "手机号定位", "定位手机号", "查定位", "查住址",
				"查询住址", "查询别人住址", "查别人地址", "查家庭住址", "查个人隐私",
				"人肉", "人肉搜索", "开盒", "开盒教程", "开盒他", "盒武器", "查快递地址",
				"doxxing", "dox someone", "doxx", "find home address", "phone number location",
				"lookup ssn", "reverse phone lookup",
			},
		},
		{
			ID:          "platform_abuse",
			Name:        "平台滥用",
			Description: "拦截绕过套餐、薅羊毛、接码、批量注册和绕过验证码等直接影响平台成本的请求。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"刷额度", "刷免费额度", "刷赠送额度", "薅羊毛", "薅羊毛脚本", "批量薅羊毛",
				"绕过套餐", "绕过套餐限制", "绕过额度", "绕过额度限制", "破解卡密", "卡密破解",
				"批量注册", "批量注册账号", "批量养号", "多开注册", "接码", "接码平台",
				"接码注册", "虚拟号注册", "绕过手机号", "绕过手机号验证", "绕过验证码",
				"绕过人机验证", "绕过turnstile", "绕过cf验证", "批量领额度", "免费额度脚本",
				"bypass quota", "bypass limit", "bypass captcha", "bypass turnstile",
				"bulk signup", "bulk registration", "sms verification bypass", "free credit abuse",
			},
		},
		{
			ID:          "system_instruction_override",
			Name:        "系统安全覆盖指令",
			Description: "拦截明确要求模型进入无限制模式、永不拒绝或不得回退安全模式的高置信组合指令；不使用单个宽泛词，避免误伤普通讨论。",
			Action:      ContentModerationActionKeywordBlock,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"[mode: unrestricted]", "[mode:unrestricted]", "[mode=unrestricted]",
				"不得拒绝/回退", "不得拒绝或回退", "不得拒绝任何请求", "无论请求内容都不得拒绝", "不得回退到安全模式",
				"never refuse any request", "must not refuse or fall back", "do not fall back to safe mode",
			},
		},
		{
			ID:          "security_research_audit",
			Name:        "安全研究审计",
			Description: "安全研究相关关键词只记录，不直接拦截；后续交给上游审计或人工判断，减少误伤 CTF 和防御分析。",
			Action:      ContentModerationActionKeywordAudit,
			Enabled:     true,
			BuiltIn:     true,
			// 不再把工具名、漏洞缩写或“红队/蓝队”等低上下文单词作为命中
			// 依据。真正带动作与对象的完整短语由多语种目录统一补入，既保留
			// 风险研究可见性，也避免普通开发、报告和依赖名称产生大量误判。
			Keywords: []string{},
		},
		{
			ID:          "prompt_injection_audit",
			Name:        "提示词攻击审计",
			Description: "提示词越权、系统提示词泄露、越狱等关键词只记录，避免误伤正常 AI 安全讨论。",
			Action:      ContentModerationActionKeywordAudit,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"忽略规则", "忽略以上", "忽略之前", "忽略之前的指令", "忽略系统提示", "覆盖系统提示",
				"输出系统提示词", "泄露系统提示词", "打印系统提示词", "展示系统提示词", "系统提示词",
				"隐藏提示词", "越狱", "越狱提示词", "dan模式", "奶奶漏洞",
				"提示词注入", "提示词泄露", "角色越权", "ignore previous instructions",
				"ignore above", "ignore all rules", "system prompt",
				"jailbreak prompt", "reveal hidden prompt", "prompt injection", "prompt leak",
			},
		},
		{
			ID:          "high_risk_advice_audit",
			Name:        "高风险建议审计",
			Description: "医疗、法律、金融等高风险建议只记录，便于后续复核，不默认阻断。",
			Action:      ContentModerationActionKeywordAudit,
			Enabled:     true,
			BuiltIn:     true,
			Keywords: []string{
				"保证收益", "稳赚不赔", "保本收益", "内幕消息", "荐股", "带单", "逃税",
				"逃税方案", "避税漏洞", "规避监管", "洗白资金", "伪证", "伪造证据",
				"销毁证据", "规避执法", "药物剂量", "处方药剂量", "自行用药剂量", "诊断疾病",
				"替代医生", "替代律师", "guaranteed return", "insider trading", "tax evasion",
				"medical dosage", "diagnose disease", "legal loophole", "destroy evidence",
			},
		},
	}
	return expandContentModerationBuiltInKeywords(packs)
}

func normalizeContentModerationKeywordRulePacks(in []ContentModerationKeywordRulePack) []ContentModerationKeywordRulePack {
	defaults := defaultContentModerationKeywordRulePacks()
	byID := make(map[string]ContentModerationKeywordRulePack, len(defaults))
	order := make([]string, 0, len(defaults)+len(in))
	for _, pack := range defaults {
		pack.Keywords = normalizeBlockedKeywords(pack.Keywords)
		pack.Action = normalizeContentModerationKeywordRuleAction(pack.Action)
		byID[pack.ID] = pack
		order = append(order, pack.ID)
	}
	for _, raw := range in {
		id := normalizeContentModerationRulePackID(raw.ID)
		if id == "" {
			continue
		}
		if existing, ok := byID[id]; ok && existing.BuiltIn {
			existing.Enabled = raw.Enabled
			if action := normalizeContentModerationKeywordRuleAction(raw.Action); action != "" {
				existing.Action = action
			}
			byID[id] = existing
			continue
		}
		pack := ContentModerationKeywordRulePack{
			ID:          id,
			Name:        trimRunes(strings.TrimSpace(raw.Name), 80),
			Description: trimRunes(strings.TrimSpace(raw.Description), 240),
			Action:      normalizeContentModerationKeywordRuleAction(raw.Action),
			Enabled:     raw.Enabled,
			Keywords:    normalizeBlockedKeywords(raw.Keywords),
			// A remotely supplied stable pack may not exist in this binary's
			// embedded defaults yet. Preserve its marker so a later catalog
			// removal does not accidentally turn stale remote content into a
			// permanent local custom blocking rule.
			BuiltIn: raw.BuiltIn,
		}
		if pack.Name == "" {
			pack.Name = pack.ID
		}
		if pack.Action == "" {
			pack.Action = ContentModerationActionKeywordBlock
		}
		if len(pack.Keywords) == 0 && !pack.BuiltIn {
			continue
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = pack
		if len(order) >= maxContentModerationKeywordRulePacks {
			break
		}
	}
	out := make([]ContentModerationKeywordRulePack, 0, len(order))
	seen := make(map[string]struct{}, len(order))
	for _, id := range order {
		if _, ok := seen[id]; ok {
			continue
		}
		pack, ok := byID[id]
		if !ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, pack)
		if len(out) >= maxContentModerationKeywordRulePacks {
			break
		}
	}
	return out
}

func cloneContentModerationKeywordRulePacks(in []ContentModerationKeywordRulePack) []ContentModerationKeywordRulePack {
	if len(in) == 0 {
		return []ContentModerationKeywordRulePack{}
	}
	out := make([]ContentModerationKeywordRulePack, 0, len(in))
	for _, pack := range in {
		clone := pack
		clone.Keywords = append([]string(nil), pack.Keywords...)
		out = append(out, clone)
	}
	return out
}

func cloneContentModerationKeywordRulePackOverrides(in []ContentModerationKeywordRulePackOverride) []ContentModerationKeywordRulePackOverride {
	out := make([]ContentModerationKeywordRulePackOverride, 0, len(in))
	for _, override := range in {
		clone := override
		if override.Enabled != nil {
			value := *override.Enabled
			clone.Enabled = &value
		}
		if override.Action != nil {
			value := *override.Action
			clone.Action = &value
		}
		out = append(out, clone)
	}
	return out
}

func normalizeContentModerationKeywordRulePackOverrides(in []ContentModerationKeywordRulePackOverride) []ContentModerationKeywordRulePackOverride {
	out := make([]ContentModerationKeywordRulePackOverride, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		id := normalizeContentModerationRulePackID(raw.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		var action *string
		if raw.Action != nil {
			normalized := normalizeContentModerationKeywordRuleAction(*raw.Action)
			if normalized != "" {
				action = &normalized
			}
		}
		if raw.Enabled == nil && action == nil {
			continue
		}
		override := ContentModerationKeywordRulePackOverride{ID: id, Action: action}
		if raw.Enabled != nil {
			value := *raw.Enabled
			override.Enabled = &value
		}
		seen[id] = struct{}{}
		out = append(out, override)
		if len(out) >= maxContentModerationKeywordRulePacks {
			break
		}
	}
	return out
}

func buildContentModerationKeywordRulePackOverrides(base, submitted []ContentModerationKeywordRulePack) []ContentModerationKeywordRulePackOverride {
	baseByID := make(map[string]ContentModerationKeywordRulePack, len(base))
	for _, pack := range base {
		baseByID[normalizeContentModerationRulePackID(pack.ID)] = pack
	}
	overrides := make([]ContentModerationKeywordRulePackOverride, 0, len(submitted))
	for _, pack := range submitted {
		id := normalizeContentModerationRulePackID(pack.ID)
		basePack, builtIn := baseByID[id]
		if id == "" || !builtIn {
			continue
		}
		override := ContentModerationKeywordRulePackOverride{ID: id}
		if pack.Enabled != basePack.Enabled {
			value := pack.Enabled
			override.Enabled = &value
		}
		action := normalizeContentModerationKeywordRuleAction(pack.Action)
		baseAction := normalizeContentModerationKeywordRuleAction(basePack.Action)
		if action != "" && action != baseAction {
			override.Action = &action
		}
		if override.Enabled != nil || override.Action != nil {
			overrides = append(overrides, override)
		}
	}
	return normalizeContentModerationKeywordRulePackOverrides(overrides)
}

func normalizeContentModerationRulePackID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune('_')
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

func normalizeContentModerationKeywordRuleAction(action string) string {
	switch strings.TrimSpace(action) {
	case ContentModerationActionKeywordBlock, "block":
		return ContentModerationActionKeywordBlock
	case ContentModerationActionKeywordAudit, "audit":
		return ContentModerationActionKeywordAudit
	default:
		return ""
	}
}

func normalizeKeywordBlockingMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case ContentModerationKeywordModeAIOnly:
		return ContentModerationKeywordModeAIOnly
	case ContentModerationKeywordModeKeywordOnly:
		return ContentModerationKeywordModeKeywordOnly
	case ContentModerationKeywordModeAPIOnly:
		return ContentModerationKeywordModeAPIOnly
	case ContentModerationKeywordModeKeywordAndAPI:
		return ContentModerationKeywordModeKeywordAndAPI
	default:
		return ContentModerationKeywordModeKeywordAndAPI
	}
}

func normalizeContentModerationModelFilter(filter ContentModerationModelFilter) ContentModerationModelFilter {
	out := ContentModerationModelFilter{
		Type:   normalizeContentModerationModelFilterType(filter.Type),
		Models: normalizeContentModerationModelNames(filter.Models),
	}
	if out.Type == ContentModerationModelFilterAll {
		out.Models = []string{}
	}
	return out
}

func cloneContentModerationModelFilter(filter ContentModerationModelFilter) ContentModerationModelFilter {
	normalized := normalizeContentModerationModelFilter(filter)
	normalized.Models = append([]string(nil), normalized.Models...)
	return normalized
}

func normalizeContentModerationModelFilterType(filterType string) string {
	switch strings.ToLower(strings.TrimSpace(filterType)) {
	case ContentModerationModelFilterInclude:
		return ContentModerationModelFilterInclude
	case ContentModerationModelFilterExclude:
		return ContentModerationModelFilterExclude
	case ContentModerationModelFilterAll:
		return ContentModerationModelFilterAll
	default:
		return ContentModerationModelFilterAll
	}
}

func normalizeContentModerationModelNames(models []string) []string {
	if len(models) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		model := trimRunes(strings.TrimSpace(raw), maxContentModerationModelFilterRunes)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
		if len(out) >= maxContentModerationModelFilterModels {
			break
		}
	}
	return out
}

func contentModerationModelListContains(models []string, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, candidate := range models {
		if strings.ToLower(strings.TrimSpace(candidate)) == model {
			return true
		}
	}
	return false
}

type contentModerationKeywordMatch struct {
	Keyword  string
	PackID   string
	PackName string
	Action   string
}

func matchContentModerationKeywordRules(text string, cfg *ContentModerationConfig) (*contentModerationKeywordMatch, bool) {
	if text == "" || cfg == nil {
		return nil, false
	}
	return newContentModerationKeywordRuleMatcher(cfg).Match(text)
}

func (s *contentModerationRuntimeSnapshot) matchKeywordRules(text string) (*contentModerationKeywordMatch, bool) {
	return s.matchKeywordRulesNormalized(normalizeContentPolicyText(text))
}

func (s *contentModerationRuntimeSnapshot) matchKeywordRulesNormalized(text string) (*contentModerationKeywordMatch, bool) {
	if s == nil || s.config == nil {
		return nil, false
	}
	if s.keywordRuleMatcher != nil {
		return s.keywordRuleMatcher.MatchNormalized(text)
	}
	matcher := newContentModerationKeywordRuleMatcher(s.config)
	if matcher == nil {
		return nil, false
	}
	return matcher.MatchNormalized(text)
}

func matchBlockedKeyword(text string, keywords []string) (string, bool) {
	if text == "" || len(keywords) == 0 {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			return kw, true
		}
	}
	return "", false
}

func normalizeModerationAPIKeys(keys []string) []string {
	if len(keys) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func deleteModerationAPIKeysByHash(keys []string, hashes []string) []string {
	keys = normalizeModerationAPIKeys(keys)
	deleteHashes := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = normalizeContentModerationHash(hash)
		if hash != "" {
			deleteHashes[hash] = struct{}{}
		}
	}
	if len(deleteHashes) == 0 {
		return keys
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := deleteHashes[moderationAPIKeyHash(key)]; ok {
			continue
		}
		out = append(out, key)
	}
	return out
}

func normalizeContentModerationAPIKeysMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case contentModerationAPIKeysModeReplace:
		return contentModerationAPIKeysModeReplace
	default:
		return contentModerationAPIKeysModeAppend
	}
}

func normalizeContentModerationHash(inputHash string) string {
	inputHash = strings.ToLower(strings.TrimSpace(inputHash))
	if len(inputHash) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(inputHash); err != nil {
		return ""
	}
	return inputHash
}

func cloneFloatMap(in map[string]float64) map[string]float64 {
	if in == nil {
		return map[string]float64{}
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneInt64Ptr(in *int64) *int64 {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func trimRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	// 绝大多数请求远低于上限。先只计数可以避免每次为了“确认不用截断”
	// 仍分配一份完整 []rune；只有确实超限时才寻找精确 UTF-8 边界。
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	if max >= len(text) { // 非 ASCII 文本仍可能满足字节数保护，保留快速路径。
		return text
	}
	runesSeen := 0
	for byteIndex := range text {
		if runesSeen == max {
			return text[:byteIndex]
		}
		runesSeen++
	}
	return text
}

func maskSecretTail(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 4 {
		return "****"
	}
	return strings.Repeat("*", 8) + secret[len(secret)-4:]
}

// CyberPolicyRecordInput 是一次 cyber_policy 硬阻断的风控记录入参。
type CyberPolicyRecordInput struct {
	RequestID          string
	UserID             int64
	UserEmail          string
	OrganizationID     int64
	BillingSubjectType string
	BillingSubjectID   int64
	ControlPlaneKeyID  string
	APIKeyID           int64
	APIKeyName         string
	GroupID            *int64
	GroupName          string
	Endpoint           string
	Model              string
	UpstreamMessage    string
	UpstreamBody       string
	UpstreamStatus     int
	UpstreamInTok      int
	UpstreamOutTok     int
}

// RecordCyberPolicyEvent 把一次 cyber_policy 硬阻断写入风控中心日志、计入违规计数、
// 并给用户发邮件。当前请求已由 gateway 透传给用户；本方法仅做事后记录/通知/计数。
// 受 risk_control_enabled 总开关和内容审核 group/model scope 约束；
// 不受 Enabled/Mode/sample 约束，因为这是上游已明确阻断后的事后审计。
func (s *ContentModerationService) RecordCyberPolicyEvent(ctx context.Context, in CyberPolicyRecordInput) {
	if s == nil || s.repo == nil {
		return
	}
	runtimeSnapshot, err := s.loadRuntimeSnapshot(ctx)
	if err != nil {
		slog.Warn("content_moderation.cyber_runtime_snapshot_load_failed", "error", err)
		return
	}
	if !runtimeSnapshot.riskControlEnabled {
		return
	}
	cfg := runtimeSnapshot.config
	if !cfg.includesGroup(in.GroupID) || !cfg.includesModel(in.Model) {
		return
	}
	var userID *int64
	if in.UserID > 0 {
		userID = &in.UserID
	}
	var apiKeyID *int64
	if in.APIKeyID > 0 {
		apiKeyID = &in.APIKeyID
	}
	errBody := strings.TrimSpace(in.UpstreamMessage)
	if b := strings.TrimSpace(in.UpstreamBody); b != "" {
		// 原始 body 不在此预脱敏；写入 log.Error 前由 redactContentModerationSecrets 统一脱敏。
		errBody = strings.TrimSpace(errBody + "\n" + b)
	}
	if in.UpstreamInTok > 0 || in.UpstreamOutTok > 0 {
		errBody = fmt.Sprintf("%s\nupstream_usage=in:%d,out:%d", errBody, in.UpstreamInTok, in.UpstreamOutTok)
	}
	// cyber 白名单（官方 v0.2.13 log-only 语义）：白名单用户照常留证，但不封号。
	allowlisted := runtimeSnapshot.isUserAllowlisted(in.UserID)
	cyberMode := "post_upstream"
	if allowlisted {
		cyberMode = ContentModerationModeCyberLogOnly
	}
	log := &ContentModerationLog{
		RequestID:          in.RequestID,
		UserID:             userID,
		UserEmail:          in.UserEmail,
		OrganizationID:     in.OrganizationID,
		BillingSubjectType: strings.TrimSpace(in.BillingSubjectType),
		BillingSubjectID:   in.BillingSubjectID,
		ControlPlaneKeyID:  strings.TrimSpace(in.ControlPlaneKeyID),
		APIKeyID:           apiKeyID,
		APIKeyName:         in.APIKeyName,
		GroupID:            cloneInt64Ptr(in.GroupID),
		GroupName:          in.GroupName,
		Endpoint:           in.Endpoint,
		Provider:           "openai",
		Model:              in.Model,
		Mode:               cyberMode,
		Action:             ContentModerationActionCyberPolicy,
		Flagged:            true,
		HighestCategory:    "cyber_policy",
		HighestScore:       1.0,
		Error:              trimRunes(redactContentModerationSecrets(errBody), maxModerationExcerptRunes*4),
		CreatedAt:          time.Now(),
	}
	// 两种跳过封号的情形，语义都是「本次不计入、也不封」：
	//   1) 全局开关：cyber_policy 不参与封号计数（当次不判定，历史行由
	//      CountFlaggedByUserSince 的 excludeCyberPolicy 排除）；
	//   2) 白名单用户：本次不判定；其历史行 mode 为 cyber_log_only，即使在移出
	//      白名单之后也不再被追溯计数（见计数 SQL 的 mode 过滤）。
	autoBanned := false
	if !cfg.CyberPolicyExcludeFromBanCount && !allowlisted {
		autoBanned = s.applyFlaggedAccountSideEffects(ctx, cfg, log)
	}
	log.EmailSent = false
	logPersisted := true
	if err := s.repo.CreateLog(ctx, log); err != nil {
		logPersisted = false
		slog.Warn("content_moderation.cyber_create_log_failed", "user_id", in.UserID, "error", err)
	}
	emailSent := false
	if s.emailService != nil && strings.TrimSpace(log.UserEmail) != "" {
		if err := s.sendCyberPolicyEmail(ctx, log); err != nil {
			slog.Warn("content_moderation.cyber_email_failed", "user_id", in.UserID, "error", err)
		} else {
			emailSent = true
		}
		if autoBanned {
			if err := s.sendAccountDisabledEmail(ctx, cfg, log); err != nil {
				slog.Warn("content_moderation.cyber_ban_email_failed", "user_id", in.UserID, "error", err)
			} else {
				emailSent = true
			}
		}
	}
	if logPersisted && emailSent {
		if err := s.repo.UpdateLogEmailSent(ctx, log.ID, true); err != nil {
			slog.Warn("content_moderation.cyber_update_email_sent_failed", "log_id", log.ID, "error", err)
		}
	}
}

func (s *ContentModerationService) sendCyberPolicyEmail(ctx context.Context, log *ContentModerationLog) error {
	siteName := s.siteName(ctx)
	if s.emailService.notificationEmailService != nil {
		variables := map[string]string{
			"triggered_at":     log.CreatedAt.UTC().Format(time.RFC3339),
			"model":            defaultContentModerationString(log.Model, "-"),
			"group_name":       defaultContentModerationString(log.GroupName, "-"),
			"upstream_message": defaultContentModerationString(log.Error, "-"),
		}
		err := s.emailService.notificationEmailService.Send(ctx, NotificationEmailSendInput{
			Event:          NotificationEmailEventCyberPolicyNotice,
			RecipientEmail: log.UserEmail,
			RecipientName:  emailRecipientName(log.UserEmail),
			UserID:         contentModerationEmailUserID(log),
			SourceType:     "content_moderation",
			SourceID:       contentModerationEmailSourceID(log),
			Variables:      variables,
		})
		if err == nil {
			return nil
		}
		if !shouldFallbackNotificationEmail(err) {
			return err
		}
		slog.Warn("template cyber policy email failed; falling back", "err", err.Error())
	}
	subject := fmt.Sprintf("[%s] 网络安全策略拦截 / Cyber Policy Notice", sanitizeEmailHeader(siteName))
	return s.emailService.SendEmail(ctx, log.UserEmail, subject, buildCyberPolicyNoticeEmailBody(siteName, log))
}
