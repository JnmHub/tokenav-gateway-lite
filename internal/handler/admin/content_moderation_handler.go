package admin

import (
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var contentModerationShanghaiLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

type ContentModerationHandler struct {
	service *service.ContentModerationService
}

func NewContentModerationHandler(svc *service.ContentModerationService) *ContentModerationHandler {
	return &ContentModerationHandler{service: svc}
}

type contentModerationConfigRequest struct {
	AI                          *service.UpdateContentModerationAIConfigInput `json:"ai"`
	Enabled                     *bool                                         `json:"enabled"`
	Mode                        *string                                       `json:"mode"`
	BaseURL                     *string                                       `json:"base_url"`
	Model                       *string                                       `json:"model"`
	APIKey                      *string                                       `json:"api_key"`
	APIKeys                     *[]string                                     `json:"api_keys"`
	APIKeysMode                 string                                        `json:"api_keys_mode"`
	DeleteAPIKeyHashes          *[]string                                     `json:"delete_api_key_hashes"`
	ClearAPIKey                 bool                                          `json:"clear_api_key"`
	TimeoutMS                   *int                                          `json:"timeout_ms"`
	SampleRate                  *int                                          `json:"sample_rate"`
	AllGroups                   *bool                                         `json:"all_groups"`
	GroupIDs                    *[]int64                                      `json:"group_ids"`
	RecordNonHits               *bool                                         `json:"record_non_hits"`
	Thresholds                  *map[string]float64                           `json:"thresholds"`
	WorkerCount                 *int                                          `json:"worker_count"`
	QueueSize                   *int                                          `json:"queue_size"`
	BlockStatus                 *int                                          `json:"block_status"`
	BlockMessage                *string                                       `json:"block_message"`
	EmailOnHit                  *bool                                         `json:"email_on_hit"`
	AutoBanEnabled              *bool                                         `json:"auto_ban_enabled"`
	BanThreshold                *int                                          `json:"ban_threshold"`
	DeductBalanceOnBlockEnabled *bool                                         `json:"deduct_balance_on_block_enabled"`
	DeductBalanceOnBlockAmount  *float64                                      `json:"deduct_balance_on_block_amount"`
	PermanentBanEnabled         *bool                                         `json:"permanent_ban_enabled"`
	PermanentBanThreshold       *int                                          `json:"permanent_ban_threshold"`
	ViolationWindowHours        *int                                          `json:"violation_window_hours"`
	// cyber_policy 命中是否排除出自动封号计数；前端 RiskControlView 已发送该字段，
	// service.UpdateContentModerationConfigInput 已支持，此前 handler 层缺透传导致开关静默失效。
	CyberPolicyExcludeFromBanCount *bool                                       `json:"cyber_policy_exclude_from_ban_count"`
	RetryCount                     *int                                        `json:"retry_count"`
	HitRetentionDays               *int                                        `json:"hit_retention_days"`
	NonHitRetentionDays            *int                                        `json:"non_hit_retention_days"`
	PreHashCheckEnabled            *bool                                       `json:"pre_hash_check_enabled"`
	BlockedKeywords                *[]string                                   `json:"blocked_keywords"`
	KeywordBlockingMode            *string                                     `json:"keyword_blocking_mode"`
	KeywordRulePacks               *[]service.ContentModerationKeywordRulePack `json:"keyword_rule_packs"`
	ModelFilter                    *service.ContentModerationModelFilter       `json:"model_filter"`
	AllowedLanguages               *[]string                                   `json:"allowed_languages"`
	ScanLatestUserInput            *bool                                       `json:"scan_latest_user_input"`
	ScanTopLevelInstructions       *bool                                       `json:"scan_top_level_instructions"`
	ScanNewSkillContent            *bool                                       `json:"scan_new_skill_content"`
	ScanNewToolOutput              *bool                                       `json:"scan_new_tool_output"`
	KeywordGroupRoutingEnabled     *bool                                       `json:"keyword_group_routing_enabled"`
}

type contentModerationAPIKeyTestRequest struct {
	APIKeys   []string `json:"api_keys"`
	BaseURL   string   `json:"base_url"`
	Model     string   `json:"model"`
	TimeoutMS int      `json:"timeout_ms"`
	Prompt    string   `json:"prompt"`
	Images    []string `json:"images"`
}

type contentModerationHashRequest struct {
	InputHash string `json:"input_hash"`
}

func (h *ContentModerationHandler) GetConfig(c *gin.Context) {
	cfg, err := h.service.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *ContentModerationHandler) UpdateConfig(c *gin.Context) {
	var req contentModerationConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.service.UpdateConfig(c.Request.Context(), service.UpdateContentModerationConfigInput{
		AI:                             req.AI,
		Enabled:                        req.Enabled,
		Mode:                           req.Mode,
		BaseURL:                        req.BaseURL,
		Model:                          req.Model,
		APIKey:                         req.APIKey,
		APIKeys:                        req.APIKeys,
		APIKeysMode:                    req.APIKeysMode,
		DeleteAPIKeyHashes:             req.DeleteAPIKeyHashes,
		ClearAPIKey:                    req.ClearAPIKey,
		TimeoutMS:                      req.TimeoutMS,
		SampleRate:                     req.SampleRate,
		AllGroups:                      req.AllGroups,
		GroupIDs:                       req.GroupIDs,
		RecordNonHits:                  req.RecordNonHits,
		Thresholds:                     req.Thresholds,
		WorkerCount:                    req.WorkerCount,
		QueueSize:                      req.QueueSize,
		BlockStatus:                    req.BlockStatus,
		BlockMessage:                   req.BlockMessage,
		EmailOnHit:                     req.EmailOnHit,
		AutoBanEnabled:                 req.AutoBanEnabled,
		BanThreshold:                   req.BanThreshold,
		DeductBalanceOnBlockEnabled:    req.DeductBalanceOnBlockEnabled,
		DeductBalanceOnBlockAmount:     req.DeductBalanceOnBlockAmount,
		PermanentBanEnabled:            req.PermanentBanEnabled,
		PermanentBanThreshold:          req.PermanentBanThreshold,
		ViolationWindowHours:           req.ViolationWindowHours,
		CyberPolicyExcludeFromBanCount: req.CyberPolicyExcludeFromBanCount,
		RetryCount:                     req.RetryCount,
		HitRetentionDays:               req.HitRetentionDays,
		NonHitRetentionDays:            req.NonHitRetentionDays,
		PreHashCheckEnabled:            req.PreHashCheckEnabled,
		BlockedKeywords:                req.BlockedKeywords,
		KeywordBlockingMode:            req.KeywordBlockingMode,
		KeywordRulePacks:               req.KeywordRulePacks,
		ModelFilter:                    req.ModelFilter,
		AllowedLanguages:               req.AllowedLanguages,
		ScanLatestUserInput:            req.ScanLatestUserInput,
		ScanTopLevelInstructions:       req.ScanTopLevelInstructions,
		ScanNewSkillContent:            req.ScanNewSkillContent,
		ScanNewToolOutput:              req.ScanNewToolOutput,
		KeywordGroupRoutingEnabled:     req.KeywordGroupRoutingEnabled,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *ContentModerationHandler) TestAPIKeys(c *gin.Context) {
	var req contentModerationAPIKeyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.TestAPIKeys(c.Request.Context(), service.TestContentModerationAPIKeysInput{
		APIKeys:   req.APIKeys,
		BaseURL:   req.BaseURL,
		Model:     req.Model,
		TimeoutMS: req.TimeoutMS,
		Prompt:    req.Prompt,
		Images:    req.Images,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *ContentModerationHandler) GetStatus(c *gin.Context) {
	status, err := h.service.GetStatus(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// GetKeywordCatalogStatus 返回公开关键词目录的当前来源和同步结果。
func (h *ContentModerationHandler) GetKeywordCatalogStatus(c *gin.Context) {
	response.Success(c, h.service.KeywordCatalogStatus())
}

// SyncKeywordCatalog 仅在目录正文通过大小、结构和 SHA-256（文件摘要）校验后切换。
func (h *ContentModerationHandler) SyncKeywordCatalog(c *gin.Context) {
	status, err := h.service.ForceKeywordCatalogUpdate(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("KEYWORD_CATALOG_SYNC_FAILED", "关键词目录同步失败："+err.Error()))
		return
	}
	response.Success(c, status)
}

func (h *ContentModerationHandler) ListLogs(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	filter := service.ContentModerationLogFilter{
		Pagination: pagination.PaginationParams{
			Page:      page,
			PageSize:  pageSize,
			SortOrder: pagination.SortOrderDesc,
		},
		Result:           c.Query("result"),
		Endpoint:         c.Query("endpoint"),
		Search:           c.Query("search"),
		ContentSource:    strings.TrimSpace(c.Query("content_source")),
		DetectedLanguage: strings.TrimSpace(c.Query("detected_language")),
		RouteReason:      strings.TrimSpace(c.Query("route_reason")),
	}
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		groupID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || groupID <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		filter.GroupID = &groupID
	}
	for queryName, target := range map[string]**int64{
		"original_group_id":  &filter.OriginalGroupID,
		"effective_group_id": &filter.EffectiveGroupID,
	} {
		if raw := strings.TrimSpace(c.Query(queryName)); raw != "" {
			groupID, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || groupID <= 0 {
				response.BadRequest(c, "Invalid "+queryName)
				return
			}
			*target = &groupID
		}
	}
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		t, _, err := parseContentModerationDate(raw)
		if err != nil {
			response.BadRequest(c, "Invalid from")
			return
		}
		filter.From = &t
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		t, dateOnly, err := parseContentModerationDate(raw)
		if err != nil {
			response.BadRequest(c, "Invalid to")
			return
		}
		if dateOnly {
			t = contentModerationEndOfDay(t)
		}
		filter.To = &t
	}
	items, pageResult, err := h.service.ListLogs(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, pageResult.Total, pageResult.Page, pageResult.PageSize)
}

func (h *ContentModerationHandler) GetLog(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid log id")
		return
	}
	item, err := h.service.GetLog(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if item == nil {
		response.NotFound(c, "Content moderation log not found")
		return
	}
	// 全文只通过已有管理员鉴权下的详情路由读取，列表保持轻量。
	response.Success(c, item)
}

func (h *ContentModerationHandler) UnbanUser(c *gin.Context) {
	userID, err := strconv.ParseInt(strings.TrimSpace(c.Param("user_id")), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user_id")
		return
	}
	result, err := h.service.UnbanUser(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *ContentModerationHandler) DeleteFlaggedHash(c *gin.Context) {
	var req contentModerationHashRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.DeleteFlaggedInputHash(c.Request.Context(), req.InputHash)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *ContentModerationHandler) ClearFlaggedHashes(c *gin.Context) {
	result, err := h.service.ClearFlaggedInputHashes(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func parseContentModerationDate(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, false, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, contentModerationShanghaiLocation)
	return t, err == nil, err
}

func contentModerationEndOfDay(day time.Time) time.Time {
	return day.AddDate(0, 0, 1).Add(-time.Nanosecond)
}
