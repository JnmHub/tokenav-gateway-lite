package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GroupHandler handles admin group management
type GroupHandler struct {
	adminService         service.AdminService
	dashboardService     *service.DashboardService
	groupCapacityService *service.GroupCapacityService
	channelService       *service.ChannelService
}

type groupAccountBinder interface {
	BindAccountsToGroup(ctx context.Context, groupID int64, accountIDs []int64, confirmMixedChannelRisk bool) ([]int64, error)
}

type optionalLimitField struct {
	set   bool
	value *float64
}

// optionalPriceField 区分“未提交”和“明确清空”。更新接口中的 JSON null
// 会转换为负数哨兵，交给 service.normalizePrice 还原为 nil，从而真正清除覆盖价。
type optionalPriceField struct {
	set   bool
	value *float64
}

// optionalTierField 区分“未提交”和“显式 null”。这样主站可用 null
// 把分组恢复为继承节点等级，同时旧客户端省略字段时不会误清空配置。
type optionalTierField struct {
	set   bool
	value *int
}

func (f *optionalTierField) UnmarshalJSON(data []byte) error {
	f.set = true
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		f.value = nil
		return nil
	}
	var tier int
	if err := json.Unmarshal(trimmed, &tier); err != nil {
		return fmt.Errorf("invalid minimum_tier: %w", err)
	}
	f.value = &tier
	return nil
}

func (f *optionalPriceField) UnmarshalJSON(data []byte) error {
	var parsed optionalLimitField
	if err := parsed.UnmarshalJSON(data); err != nil {
		return err
	}
	f.set = parsed.set
	f.value = parsed.value
	return nil
}

func (f optionalPriceField) ToServiceInput() *float64 {
	if !f.set {
		return nil
	}
	if f.value != nil {
		return f.value
	}
	clearSentinel := -1.0
	return &clearSentinel
}

func (f *optionalLimitField) UnmarshalJSON(data []byte) error {
	f.set = true

	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		f.value = nil
		return nil
	}

	var number float64
	if err := json.Unmarshal(trimmed, &number); err == nil {
		f.value = &number
		return nil
	}

	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			f.value = nil
			return nil
		}
		number, err = strconv.ParseFloat(text, 64)
		if err != nil {
			return fmt.Errorf("invalid numeric limit value %q: %w", text, err)
		}
		f.value = &number
		return nil
	}

	return fmt.Errorf("invalid limit value: %s", string(trimmed))
}

func (f optionalLimitField) ToServiceInput() *float64 {
	if !f.set {
		return nil
	}
	if f.value != nil {
		return f.value
	}
	zero := 0.0
	return &zero
}

// NewGroupHandler creates a new admin group handler
func NewGroupHandler(adminService service.AdminService, dashboardService *service.DashboardService, groupCapacityService *service.GroupCapacityService, channelService *service.ChannelService) *GroupHandler {
	return &GroupHandler{
		adminService:         adminService,
		dashboardService:     dashboardService,
		groupCapacityService: groupCapacityService,
		channelService:       channelService,
	}
}

// CreateGroupRequest represents create group request
type CreateGroupRequest struct {
	Name               string             `json:"name" binding:"required"`
	Description        string             `json:"description"`
	Platform           string             `json:"platform" binding:"omitempty,oneof=anthropic openai gemini antigravity grok kimi zhipu deepseek composite"`
	DisplayBrand       string             `json:"display_brand"`
	DisplayIcon        string             `json:"display_icon"`
	RateMultiplier     float64            `json:"rate_multiplier"`
	PeakRateEnabled    bool               `json:"peak_rate_enabled"`
	PeakStart          string             `json:"peak_start"`
	PeakEnd            string             `json:"peak_end"`
	PeakRateMultiplier float64            `json:"peak_rate_multiplier"`
	PeakTimezone       string             `json:"peak_timezone"`
	IsExclusive        bool               `json:"is_exclusive"`
	LocalOnly          bool               `json:"local_only"`
	MinimumTier        *int               `json:"minimum_tier"`
	SchedulingMode     string             `json:"scheduling_mode" binding:"omitempty,oneof=default random strict_priority"`
	PerUserConcurrency int                `json:"per_user_concurrency" binding:"omitempty,min=0,max=100000"`
	Status             string             `json:"status" binding:"omitempty,oneof=active inactive"`
	SubscriptionType   string             `json:"subscription_type" binding:"omitempty,oneof=standard subscription"`
	DailyLimitUSD      optionalLimitField `json:"daily_limit_usd"`
	WeeklyLimitUSD     optionalLimitField `json:"weekly_limit_usd"`
	MonthlyLimitUSD    optionalLimitField `json:"monthly_limit_usd"`
	// 图片生成计费配置（antigravity 和 gemini 平台使用，负数表示清除配置）
	AllowImageGeneration            bool                          `json:"allow_image_generation"`
	ImageRateIndependent            bool                          `json:"image_rate_independent"`
	ImageRateMultiplier             *float64                      `json:"image_rate_multiplier"`
	ImagePrice1K                    *float64                      `json:"image_price_1k"`
	ImagePrice2K                    *float64                      `json:"image_price_2k"`
	ImagePrice4K                    *float64                      `json:"image_price_4k"`
	VideoRateIndependent            bool                          `json:"video_rate_independent"`
	VideoRateMultiplier             *float64                      `json:"video_rate_multiplier"`
	VideoPrice480P                  *float64                      `json:"video_price_480p"`
	VideoPrice720P                  *float64                      `json:"video_price_720p"`
	VideoPrice1080P                 *float64                      `json:"video_price_1080p"`
	VideoModelPrices                map[string]map[string]float64 `json:"video_model_prices,omitempty"`
	WebSearchPricePerCall           *float64                      `json:"web_search_price_per_call"`
	SearchPricePer1k                *float64                      `json:"search_price_per_1k"`
	AudioRealtimePricePerMin        *float64                      `json:"audio_realtime_price_per_min"`
	AudioTtsPricePerMillionChars    *float64                      `json:"audio_tts_price_per_million_chars"`
	AudioSttPricePerHour            *float64                      `json:"audio_stt_price_per_hour"`
	AllowOpenAILongContextBilling   bool                          `json:"allow_openai_long_context_billing"`
	AllowBlockedKeywordRequests     bool                          `json:"allow_blocked_keyword_requests"`
	ClaudeCodeOnly                  bool                          `json:"claude_code_only"`
	FallbackGroupID                 *int64                        `json:"fallback_group_id"`
	FallbackGroupIDOnInvalidRequest *int64                        `json:"fallback_group_id_on_invalid_request"`
	FailoverGroupID                 *int64                        `json:"failover_group_id"`
	FailoverStrategy                string                        `json:"failover_strategy"`
	// 模型路由配置（仅 anthropic 平台使用）
	ModelRouting        map[string][]int64 `json:"model_routing"`
	ModelRoutingEnabled bool               `json:"model_routing_enabled"`
	ModelMapping        map[string]string  `json:"model_mapping"`
	// 智能分流映射：公开模型 -> [{model,weight}]；与 model_mapping 键互斥
	WeightedModelMapping map[string][]service.WeightedModelTarget `json:"weighted_model_mapping"`
	FallbackModel         string                                    `json:"fallback_model"`
	BillingModelSource  string             `json:"billing_model_source" binding:"omitempty,oneof=requested channel_mapped upstream response_model"`
	MCPXMLInject        *bool              `json:"mcp_xml_inject"`
	// 支持的模型系列（仅 antigravity 平台使用）
	SupportedModelScopes []string `json:"supported_model_scopes"`
	// OpenAI Messages 调度配置（仅 openai 平台使用）
	AllowMessagesDispatch       bool                                      `json:"allow_messages_dispatch"`
	AllowLive                   bool                                      `json:"allow_live"`
	RequireOAuthOnly            bool                                      `json:"require_oauth_only"`
	RequirePrivacySet           bool                                      `json:"require_privacy_set"`
	DefaultMappedModel          string                                    `json:"default_mapped_model"`
	MessagesDispatchModelConfig service.OpenAIMessagesDispatchModelConfig `json:"messages_dispatch_model_config"`
	ModelsListConfig            service.GroupModelsListConfig             `json:"models_list_config"`
	// 分组 RPM 上限（0 = 不限制）
	RPMLimit                int                              `json:"rpm_limit"`
	MaxReasoningEffort      string                           `json:"max_reasoning_effort"`
	ReasoningEffortMappings []service.ReasoningEffortMapping `json:"reasoning_effort_mappings"`
	// 从指定分组复制账号（创建后自动绑定）
	CopyAccountsFromGroupIDs []int64 `json:"copy_accounts_from_group_ids"`
// [v0.2.5] 利润控制
ProfitControlEnabled bool    `json:"profit_control_enabled"`
ProfitMinMargin      float64 `json:"profit_min_margin"`
ProfitSafetyBuffer   float64 `json:"profit_safety_buffer"`
}

// UpdateGroupRequest represents update group request
type UpdateGroupRequest struct {
	Name               string             `json:"name"`
	Description        *string            `json:"description"`
	Platform           string             `json:"platform" binding:"omitempty,oneof=anthropic openai gemini antigravity grok kimi zhipu deepseek composite"`
	DisplayBrand       *string            `json:"display_brand"`
	DisplayIcon        *string            `json:"display_icon"`
	RateMultiplier     *float64           `json:"rate_multiplier"`
	PeakRateEnabled    *bool              `json:"peak_rate_enabled"`
	PeakStart          *string            `json:"peak_start"`
	PeakEnd            *string            `json:"peak_end"`
	PeakRateMultiplier *float64           `json:"peak_rate_multiplier"`
	PeakTimezone       *string            `json:"peak_timezone"`
	IsExclusive        *bool              `json:"is_exclusive"`
	LocalOnly          *bool              `json:"local_only"`
	MinimumTier        optionalTierField  `json:"minimum_tier"`
	SchedulingMode     *string            `json:"scheduling_mode" binding:"omitempty,oneof=default random strict_priority"`
	PerUserConcurrency *int               `json:"per_user_concurrency" binding:"omitempty,min=0,max=100000"`
	Status             string             `json:"status" binding:"omitempty,oneof=active inactive"`
	SubscriptionType   string             `json:"subscription_type" binding:"omitempty,oneof=standard subscription"`
	DailyLimitUSD      optionalLimitField `json:"daily_limit_usd"`
	WeeklyLimitUSD     optionalLimitField `json:"weekly_limit_usd"`
	MonthlyLimitUSD    optionalLimitField `json:"monthly_limit_usd"`
	// 图片生成计费配置（antigravity 和 gemini 平台使用，负数表示清除配置）
	AllowImageGeneration            *bool                         `json:"allow_image_generation"`
	ImageRateIndependent            *bool                         `json:"image_rate_independent"`
	ImageRateMultiplier             *float64                      `json:"image_rate_multiplier"`
	ImagePrice1K                    optionalPriceField            `json:"image_price_1k"`
	ImagePrice2K                    optionalPriceField            `json:"image_price_2k"`
	ImagePrice4K                    optionalPriceField            `json:"image_price_4k"`
	VideoRateIndependent            *bool                         `json:"video_rate_independent"`
	VideoRateMultiplier             *float64                      `json:"video_rate_multiplier"`
	VideoPrice480P                  optionalPriceField            `json:"video_price_480p"`
	VideoPrice720P                  optionalPriceField            `json:"video_price_720p"`
	VideoPrice1080P                 optionalPriceField            `json:"video_price_1080p"`
	VideoModelPrices                map[string]map[string]float64 `json:"video_model_prices,omitempty"`
	WebSearchPricePerCall           optionalPriceField            `json:"web_search_price_per_call"`
	SearchPricePer1k                optionalPriceField            `json:"search_price_per_1k"`
	AudioRealtimePricePerMin        optionalPriceField            `json:"audio_realtime_price_per_min"`
	AudioTtsPricePerMillionChars    optionalPriceField            `json:"audio_tts_price_per_million_chars"`
	AudioSttPricePerHour            optionalPriceField            `json:"audio_stt_price_per_hour"`
	AllowOpenAILongContextBilling   *bool                         `json:"allow_openai_long_context_billing"`
	AllowBlockedKeywordRequests     *bool                         `json:"allow_blocked_keyword_requests"`
	ClaudeCodeOnly                  *bool                         `json:"claude_code_only"`
	FallbackGroupID                 *int64                        `json:"fallback_group_id"`
	FallbackGroupIDOnInvalidRequest *int64                        `json:"fallback_group_id_on_invalid_request"`
	FailoverGroupID                 *int64                        `json:"failover_group_id"`
	FailoverStrategy                *string                       `json:"failover_strategy"`
	// 模型路由配置（仅 anthropic 平台使用）
	ModelRouting        map[string][]int64 `json:"model_routing"`
	ModelRoutingEnabled *bool              `json:"model_routing_enabled"`
	ModelMapping        map[string]string  `json:"model_mapping"`
	// 智能分流映射；nil 不修改，空对象清空
	WeightedModelMapping map[string][]service.WeightedModelTarget `json:"weighted_model_mapping"`
	FallbackModel         *string                                   `json:"fallback_model"`
	BillingModelSource  *string            `json:"billing_model_source" binding:"omitempty,oneof=requested channel_mapped upstream response_model"`
	MCPXMLInject        *bool              `json:"mcp_xml_inject"`
	// 支持的模型系列（仅 antigravity 平台使用）
	SupportedModelScopes *[]string `json:"supported_model_scopes"`
	// OpenAI Messages 调度配置（仅 openai 平台使用）
	AllowMessagesDispatch       *bool                                      `json:"allow_messages_dispatch"`
	AllowLive                   *bool                                      `json:"allow_live"`
	RequireOAuthOnly            *bool                                      `json:"require_oauth_only"`
	RequirePrivacySet           *bool                                      `json:"require_privacy_set"`
	DefaultMappedModel          *string                                    `json:"default_mapped_model"`
	MessagesDispatchModelConfig *service.OpenAIMessagesDispatchModelConfig `json:"messages_dispatch_model_config"`
	ModelsListConfig            *service.GroupModelsListConfig             `json:"models_list_config"`
	// 分组 RPM 上限（0 = 不限制）；nil 表示未提供不改动
	RPMLimit                *int                              `json:"rpm_limit"`
	MaxReasoningEffort      *string                           `json:"max_reasoning_effort"`
	ReasoningEffortMappings *[]service.ReasoningEffortMapping `json:"reasoning_effort_mappings"`
	// 从指定分组复制账号（同步操作：先清空当前分组的账号绑定，再绑定源分组的账号）
	CopyAccountsFromGroupIDs []int64 `json:"copy_accounts_from_group_ids"`
// [v0.2.5] 利润控制
ProfitControlEnabled *bool    `json:"profit_control_enabled"`
ProfitMinMargin      *float64 `json:"profit_min_margin"`
ProfitSafetyBuffer   *float64 `json:"profit_safety_buffer"`
}

type CompositeRouteRequest struct {
	PublicModel    string `json:"public_model" binding:"required"`
	MatchType      string `json:"match_type" binding:"omitempty,oneof=exact prefix"`
	TargetPlatform string `json:"target_platform" binding:"required,oneof=anthropic openai gemini antigravity grok kimi zhipu deepseek"`
	UpstreamModel  string `json:"upstream_model"`
	Endpoint       string `json:"endpoint" binding:"omitempty,oneof=any messages count_tokens responses chat_completions embeddings images gemini"`
	Priority       int    `json:"priority"`
	Enabled        *bool  `json:"enabled"`
	Notes          string `json:"notes"`
}

type CompositeRoutePreviewRequest struct {
	Model          string                 `json:"model" binding:"required"`
	Endpoint       string                 `json:"endpoint" binding:"omitempty,oneof=any messages count_tokens responses chat_completions embeddings images gemini"`
	Candidate      *CompositeRouteRequest `json:"candidate"`
	EditingRouteID int64                  `json:"editing_route_id"`
}

type CompositeRouteBatchStatusRequest struct {
	RouteIDs []int64 `json:"route_ids" binding:"required,min=1,dive,gt=0"`
	Enabled  bool    `json:"enabled"`
}

// BindGroupAccountsRequest binds selected existing accounts to the current
// group while preserving every other group binding on each account.
type BindGroupAccountsRequest struct {
	AccountIDs              []int64 `json:"account_ids" binding:"required,min=1,dive,gt=0"`
	ConfirmMixedChannelRisk bool    `json:"confirm_mixed_channel_risk"`
}

// BindAccounts adds the current group to selected accounts. The validation
// pass runs before any write so a mixed-platform or invalid account cannot
// leave the selection half-applied.
// POST /api/v1/admin/groups/:id/accounts/bind
func (h *GroupHandler) BindAccounts(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req BindGroupAccountsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	binder, ok := h.adminService.(groupAccountBinder)
	if !ok {
		response.Error(c, http.StatusNotImplemented, "Group account binding is not configured")
		return
	}
	bound, err := binder.BindAccountsToGroup(c.Request.Context(), groupID, req.AccountIDs, req.ConfirmMixedChannelRisk)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	reportGatewayLiteConfigChange("accounts", "bind_group", groupID)
	response.Success(c, gin.H{"group_id": groupID, "account_ids": bound, "bound": len(bound)})
}

func (h *GroupHandler) ListCompositeRoutes(c *gin.Context) {
	groupID, ok := parseCompositeIDParam(c, "id")
	if !ok {
		return
	}
	routes, err := h.adminService.ListCompositeRoutes(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, routes)
}

func (h *GroupHandler) CreateCompositeRoute(c *gin.Context) {
	groupID, ok := parseCompositeIDParam(c, "id")
	if !ok {
		return
	}
	var req CompositeRouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	route, err := h.adminService.CreateCompositeRoute(c.Request.Context(), groupID, compositeRouteRequestToInput(req, true))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	reportGatewayLiteConfigChange("composite_routes", "create", groupID)
	response.Success(c, route)
}

func (h *GroupHandler) UpdateCompositeRoute(c *gin.Context) {
	groupID, ok := parseCompositeIDParam(c, "id")
	if !ok {
		return
	}
	routeID, ok := parseCompositeIDParam(c, "route_id")
	if !ok {
		return
	}
	var req CompositeRouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	route, err := h.adminService.UpdateCompositeRoute(c.Request.Context(), groupID, routeID, compositeRouteRequestToInput(req, true))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	reportGatewayLiteConfigChange("composite_routes", "update", groupID)
	response.Success(c, route)
}

func (h *GroupHandler) DeleteCompositeRoute(c *gin.Context) {
	groupID, ok := parseCompositeIDParam(c, "id")
	if !ok {
		return
	}
	routeID, ok := parseCompositeIDParam(c, "route_id")
	if !ok {
		return
	}
	if err := h.adminService.DeleteCompositeRoute(c.Request.Context(), groupID, routeID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	reportGatewayLiteConfigChange("composite_routes", "delete", groupID)
	response.Success(c, gin.H{"message": "聚合路由已删除"})
}

func (h *GroupHandler) BatchSetCompositeRoutesEnabled(c *gin.Context) {
	groupID, ok := parseCompositeIDParam(c, "id")
	if !ok {
		return
	}
	var req CompositeRouteBatchStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.adminService.BatchSetCompositeRoutesEnabled(c.Request.Context(), groupID, req.RouteIDs, req.Enabled); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	reportGatewayLiteConfigChange("composite_routes", "batch_status", groupID)
	response.Success(c, gin.H{"updated": len(req.RouteIDs), "enabled": req.Enabled})
}

func (h *GroupHandler) PreviewCompositeRoute(c *gin.Context) {
	groupID, ok := parseCompositeIDParam(c, "id")
	if !ok {
		return
	}
	var req CompositeRoutePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	input := service.CompositeRoutePreviewRequest{Model: req.Model, Endpoint: req.Endpoint, EditingRouteID: req.EditingRouteID}
	if req.Candidate != nil {
		candidate := compositeRouteRequestToInput(*req.Candidate, true)
		input.Candidate = &candidate
	}
	decision, err := h.adminService.PreviewCompositeRoute(c.Request.Context(), groupID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, decision)
}

func compositeRouteRequestToInput(req CompositeRouteRequest, defaultEnabled bool) service.CompositeRouteInput {
	enabled := defaultEnabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return service.CompositeRouteInput{PublicModel: req.PublicModel, MatchType: req.MatchType, TargetPlatform: req.TargetPlatform,
		UpstreamModel: req.UpstreamModel, Endpoint: req.Endpoint, Priority: req.Priority, Enabled: enabled, Notes: req.Notes}
}

func parseCompositeIDParam(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid "+name)
		return 0, false
	}
	return id, true
}

// List handles listing all groups with pagination
// GET /api/v1/admin/groups
func (h *GroupHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	platform := c.Query("platform")
	status := c.Query("status")
	search := c.Query("search")
	// 标准化和验证 search 参数
	search = strings.TrimSpace(search)
	if len(search) > 100 {
		search = search[:100]
	}
	isExclusiveStr := c.Query("is_exclusive")
	sortBy := c.DefaultQuery("sort_by", "sort_order")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	var isExclusive *bool
	if isExclusiveStr != "" {
		val := isExclusiveStr == "true"
		isExclusive = &val
	}

	requestContext := c.Request.Context()
	if c.Query("publishable_only") == "true" {
		requestContext = service.ContextWithPublishableGroupsOnly(requestContext)
	}
	groups, total, err := h.adminService.ListGroups(requestContext, page, pageSize, platform, status, search, isExclusive, sortBy, sortOrder)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	outGroups := make([]dto.AdminGroup, 0, len(groups))
	for i := range groups {
		outGroups = append(outGroups, *dto.GroupFromServiceAdmin(&groups[i]))
	}
	response.Paginated(c, outGroups, total, page, pageSize)
}

// GetAll handles getting all active groups without pagination.
// Pass ?include_inactive=true to also include disabled groups (used by the
// API Key group filter, which needs to surface groups that still have API keys
// bound to them even after the group is disabled).
// GET /api/v1/admin/groups/all
func (h *GroupHandler) GetAll(c *gin.Context) {
	platform := c.Query("platform")
	includeInactive := c.Query("include_inactive") == "true"
	publishableOnly := c.Query("publishable_only") == "true"

	var groups []service.Group
	var err error

	if includeInactive {
		groups, err = h.adminService.GetAllGroupsIncludingInactive(c.Request.Context())
	} else if platform != "" {
		groups, err = h.adminService.GetAllGroupsByPlatform(c.Request.Context(), platform)
	} else {
		groups, err = h.adminService.GetAllGroups(c.Request.Context())
	}

	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	outGroups := make([]dto.AdminGroup, 0, len(groups))
	for i := range groups {
		if publishableOnly && groups[i].LocalOnly {
			continue
		}
		outGroups = append(outGroups, *dto.GroupFromServiceAdmin(&groups[i]))
	}
	response.Success(c, outGroups)
}

// GetByID handles getting a group by ID
// GET /api/v1/admin/groups/:id
func (h *GroupHandler) GetByID(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	group, err := h.adminService.GetGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if groupHiddenFromPublishableQuery(c, group) {
		return
	}

	response.Success(c, dto.GroupFromServiceAdmin(group))
}

func groupHiddenFromPublishableQuery(c *gin.Context, group *service.Group) bool {
	if c.Query("publishable_only") != "true" || group == nil || !group.LocalOnly {
		return false
	}
	response.ErrorFrom(c, service.ErrGroupNotFound)
	return true
}

// GetEffectiveModels returns the persisted, customer-facing model set for one
// group.  This deliberately reads only the group's bound accounts and saved
// model mappings: an admin editor must never turn an unavailable runtime
// report into a guessed list of usable models.
// GET /api/v1/admin/groups/:id/effective-models
func (h *GroupHandler) GetEffectiveModels(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	group, err := h.adminService.GetGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if groupHiddenFromPublishableQuery(c, group) {
		return
	}
	// A bounded group-scoped list is sufficient here.  It avoids a global
	// account scan while still applying the same persisted active/schedulable
	// rules as the public models list.
	accounts, _, err := h.adminService.ListAccounts(
		c.Request.Context(), 1, 10_000, group.Platform, "", "", "", group.ID, "", "", "", "",
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	knownModels, err := h.adminService.GetGroupModelsListCandidates(c.Request.Context(), group.ID, group.Platform)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	models := service.EffectiveModelsForGroup(group, accounts, knownModels)
	version := group.UpdatedAt.UnixMilli()
	if version <= 0 {
		version = time.Now().UnixMilli()
	}
	response.Success(c, gin.H{
		"group_id":          group.ID,
		"models":            models,
		"effective_models":  models,
		"configured_models": group.ModelsListConfig.Models,
		"reported":          true,
		"version":           version,
	})
}

// GetModelRouteHealth returns the in-process health of every weighted routing
// target in one group. It is read-only observability: states reflect only what
// this gateway instance observed and reset on restart.
// GET /api/v1/admin/groups/:id/model-route-health
func (h *GroupHandler) GetModelRouteHealth(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	_, err = h.adminService.GetGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	views := service.ModelRouteHealthSnapshot(groupID)
	if views == nil {
		views = []service.ModelRouteHealthView{}
	}
	response.Success(c, gin.H{"group_id": groupID, "targets": views})
}

// ResetModelRouteHealth clears one weighted routing target's cooldown so it
// immediately re-enters rotation. Body: {"public_model": "...", "target_model": "..."}.
// DELETE /api/v1/admin/groups/:id/model-route-health
func (h *GroupHandler) ResetModelRouteHealth(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req struct {
		PublicModel string `json:"public_model" binding:"required"`
		TargetModel string `json:"target_model" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "public_model and target_model are required")
		return
	}
	if !service.ResetModelRouteHealth(groupID, req.PublicModel, req.TargetModel) {
		response.BadRequest(c, "No cooling state found for this route target")
		return
	}
	response.Success(c, gin.H{
		"group_id":     groupID,
		"public_model": req.PublicModel,
		"target_model": req.TargetModel,
		"reset":        true,
	})
}

// GetModelsListCandidates handles getting candidate model IDs for custom /v1/models list.
// GET /api/v1/admin/groups/:id/models-list-candidates
func (h *GroupHandler) GetModelsListCandidates(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID < 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	models, err := h.adminService.GetGroupModelsListCandidates(
		c.Request.Context(),
		groupID,
		c.Query("platform"),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"models": models})
}

// Create handles creating a new group
// POST /api/v1/admin/groups
func (h *GroupHandler) Create(c *gin.Context) {
	var req CreateGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	group, err := h.adminService.CreateGroup(c.Request.Context(), &service.CreateGroupInput{
		Name:                            req.Name,
		Description:                     req.Description,
		Platform:                        req.Platform,
		DisplayBrand:                    req.DisplayBrand,
		DisplayIcon:                     req.DisplayIcon,
		RateMultiplier:                  req.RateMultiplier,
		PeakRateEnabled:                 req.PeakRateEnabled,
		PeakStart:                       req.PeakStart,
		PeakEnd:                         req.PeakEnd,
		PeakRateMultiplier:              req.PeakRateMultiplier,
		PeakTimezone:                    req.PeakTimezone,
		IsExclusive:                     req.IsExclusive,
		LocalOnly:                       req.LocalOnly,
		MinimumTier:                     req.MinimumTier,
		SchedulingMode:                  req.SchedulingMode,
		PerUserConcurrency:              req.PerUserConcurrency,
		Status:                          req.Status,
		SubscriptionType:                req.SubscriptionType,
		DailyLimitUSD:                   req.DailyLimitUSD.ToServiceInput(),
		WeeklyLimitUSD:                  req.WeeklyLimitUSD.ToServiceInput(),
		MonthlyLimitUSD:                 req.MonthlyLimitUSD.ToServiceInput(),
		AllowImageGeneration:            req.AllowImageGeneration,
		ProfitControlEnabled:            req.ProfitControlEnabled,
		ProfitMinMargin:                 req.ProfitMinMargin,
		ProfitSafetyBuffer:              req.ProfitSafetyBuffer,
		ImageRateIndependent:            req.ImageRateIndependent,
		ImageRateMultiplier:             req.ImageRateMultiplier,
		ImagePrice1K:                    req.ImagePrice1K,
		ImagePrice2K:                    req.ImagePrice2K,
		ImagePrice4K:                    req.ImagePrice4K,
		VideoRateIndependent:            req.VideoRateIndependent,
		VideoRateMultiplier:             req.VideoRateMultiplier,
		VideoPrice480P:                  req.VideoPrice480P,
		VideoPrice720P:                  req.VideoPrice720P,
		VideoPrice1080P:                 req.VideoPrice1080P,
		VideoModelPrices:                req.VideoModelPrices,
		WebSearchPricePerCall:           req.WebSearchPricePerCall,
		SearchPricePer1k:                req.SearchPricePer1k,
		AudioRealtimePricePerMin:        req.AudioRealtimePricePerMin,
		AudioTTSPricePerMillionChars:    req.AudioTtsPricePerMillionChars,
		AudioSTTPricePerHour:            req.AudioSttPricePerHour,
		AllowOpenAILongContextBilling:   req.AllowOpenAILongContextBilling,
		AllowBlockedKeywordRequests:     req.AllowBlockedKeywordRequests,
		ClaudeCodeOnly:                  req.ClaudeCodeOnly,
		FallbackGroupID:                 req.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: req.FallbackGroupIDOnInvalidRequest,
		FailoverGroupID:                 req.FailoverGroupID,
		FailoverStrategy:                req.FailoverStrategy,
		ModelRouting:                    req.ModelRouting,
		ModelRoutingEnabled:             req.ModelRoutingEnabled,
		ModelMapping:                    req.ModelMapping,
		WeightedModelMapping:            req.WeightedModelMapping,
		FallbackModel:                   req.FallbackModel,
		BillingModelSource:              req.BillingModelSource,
		MCPXMLInject:                    req.MCPXMLInject,
		SupportedModelScopes:            req.SupportedModelScopes,
		AllowMessagesDispatch:           req.AllowMessagesDispatch,
		AllowLive:                       req.AllowLive,
		RequireOAuthOnly:                req.RequireOAuthOnly,
		RequirePrivacySet:               req.RequirePrivacySet,
		DefaultMappedModel:              req.DefaultMappedModel,
		MessagesDispatchModelConfig:     req.MessagesDispatchModelConfig,
		ModelsListConfig:                req.ModelsListConfig,
		RPMLimit:                        req.RPMLimit,
		MaxReasoningEffort:              req.MaxReasoningEffort,
		ReasoningEffortMappings:         req.ReasoningEffortMappings,
		CopyAccountsFromGroupIDs:        req.CopyAccountsFromGroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	reportGatewayLiteConfigChange("groups", "create", group.ID)
	response.Success(c, dto.GroupFromServiceAdmin(group))
}

type updateGroupPricingRequest struct {
	Enabled                    bool                             `json:"enabled"`
	ModelPricing               []channelModelPricingRequest     `json:"model_pricing"`
	RestrictModels             bool                             `json:"restrict_models"`
	FeaturesConfig             map[string]any                   `json:"features_config"`
	ApplyPricingToAccountStats bool                             `json:"apply_pricing_to_account_stats"`
	AccountStatsPricingRules   []accountStatsPricingRuleRequest `json:"account_stats_pricing_rules"`
}

// GetPricing 返回当前分组的完整计费合同；没有自定义价时明确返回 inherited（继承）。
func (h *GroupHandler) GetPricing(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	if h.channelService == nil {
		response.ErrorFrom(c, service.ErrGroupPricingRepositoryUnavailable)
		return
	}
	config, err := h.channelService.GetGroupPricing(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, groupPricingToResponse(config))
}

// UpdatePricing 在一个事务内保存当前分组价格；共享渠道会自动拆分，不影响其他分组。
func (h *GroupHandler) UpdatePricing(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	var req updateGroupPricingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}
	if h.channelService == nil {
		response.ErrorFrom(c, service.ErrGroupPricingRepositoryUnavailable)
		return
	}

	pricing := pricingRequestToService(req.ModelPricing)
	rules := make([]service.AccountStatsPricingRule, 0, len(req.AccountStatsPricingRules))
	for i, ruleReq := range req.AccountStatsPricingRules {
		rule := accountStatsPricingRuleRequestToService(ruleReq)
		rule.SortOrder = i
		rules = append(rules, rule)
	}
	config, err := h.channelService.UpdateGroupPricing(c.Request.Context(), groupID, &service.UpdateGroupPricingInput{
		Enabled:                    req.Enabled,
		ModelPricing:               pricing,
		RestrictModels:             req.RestrictModels,
		FeaturesConfig:             req.FeaturesConfig,
		ApplyPricingToAccountStats: req.ApplyPricingToAccountStats,
		AccountStatsPricingRules:   rules,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	middleware.SetAuditAction(c, "admin.groups.pricing.update")
	middleware.SetAuditResource(c, "group", strconv.FormatInt(groupID, 10))
	middleware.SetAuditSummary(c, map[string]any{"enabled": req.Enabled})
	// 分组自定义价格同时改变 groups[].model_prices（分组价格合同）和全局价格视图。
	// 使用 mixed（混合配置）可让配置主动推送与旧回退同步都刷新分组镜像，
	// 避免主站短暂继续展示保存前的计费方式或价格。
	reportGatewayLiteConfigChange("mixed", "group_pricing_update", groupID)
	response.Success(c, groupPricingToResponse(config))
}

func groupPricingToResponse(config *service.GroupPricingConfig) gin.H {
	modelPricing := make([]channelModelPricingResponse, 0, len(config.ModelPricing))
	for i := range config.ModelPricing {
		modelPricing = append(modelPricing, pricingToResponse(&config.ModelPricing[i]))
	}
	rules := make([]accountStatsPricingRuleResponse, 0, len(config.AccountStatsPricingRules))
	for _, rule := range config.AccountStatsPricingRules {
		pricing := make([]channelModelPricingResponse, 0, len(rule.Pricing))
		for i := range rule.Pricing {
			pricing = append(pricing, pricingToResponse(&rule.Pricing[i]))
		}
		rules = append(rules, accountStatsPricingRuleResponse{
			ID: rule.ID, Name: rule.Name, GroupIDs: emptyInt64Slice(rule.GroupIDs),
			AccountIDs: emptyInt64Slice(rule.AccountIDs), Pricing: pricing,
		})
	}
	return gin.H{
		"enabled": config.Enabled, "source": config.Source, "channel_id": config.ChannelID,
		"shared": config.Shared, "model_pricing": modelPricing, "restrict_models": config.RestrictModels,
		"features_config": config.FeaturesConfig, "apply_pricing_to_account_stats": config.ApplyPricingToAccountStats,
		"account_stats_pricing_rules": rules,
	}
}

func emptyInt64Slice(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	return values
}

// Duplicate 创建默认停用的分组副本，服务端原子复制配置和账号优先级。
// POST /api/v1/admin/groups/:id/duplicate
func (h *GroupHandler) Duplicate(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	operationKey, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if operationKey == "" {
		response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
		return
	}
	c.Request.Header.Set("Idempotency-Key", operationKey)
	actorScope := adminActorScope(c)

	result, err := executeAdminIdempotent(
		c,
		"admin.groups.duplicate",
		struct {
			GroupID int64 `json:"group_id"`
		}{GroupID: groupID},
		service.DefaultWriteIdempotencyTTL(),
		func(ctx context.Context) (any, error) {
			group, execErr := h.adminService.DuplicateGroup(ctx, groupID, actorScope, operationKey)
			if execErr != nil {
				return nil, execErr
			}
			return dto.GroupFromServiceAdmin(group), nil
		},
	)
	if err != nil {
		reason := infraerrors.Reason(err)
		if reason == infraerrors.Reason(service.ErrIdempotencyInProgress) || reason == infraerrors.Reason(service.ErrIdempotencyStoreUnavail) {
			recovered, recoverErr := h.adminService.RecoverDuplicateGroup(c.Request.Context(), groupID, actorScope, operationKey)
			if recoverErr != nil {
				slog.Warn("group_duplicate_recovery_failed", "group_id", groupID, "actor_scope", actorScope, "reason", reason, "error", recoverErr)
			} else if recovered != nil {
				c.Header("X-Idempotency-Recovered", "true")
				reportGatewayLiteConfigChange("groups", "duplicate", recovered.ID)
				response.Success(c, dto.GroupFromServiceAdmin(recovered))
				return
			}
		}
		if retryAfter := service.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}

	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	} else if result != nil {
		if group, ok := result.Data.(*dto.AdminGroup); ok && group != nil {
			reportGatewayLiteConfigChange("groups", "duplicate", group.ID)
		}
	}
	response.Success(c, result.Data)
}

// Update handles updating a group
// PUT /api/v1/admin/groups/:id
func (h *GroupHandler) Update(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req UpdateGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	group, err := h.adminService.UpdateGroup(c.Request.Context(), groupID, &service.UpdateGroupInput{
		Name:                            req.Name,
		Description:                     req.Description,
		Platform:                        req.Platform,
		DisplayBrand:                    req.DisplayBrand,
		DisplayIcon:                     req.DisplayIcon,
		RateMultiplier:                  req.RateMultiplier,
		PeakRateEnabled:                 req.PeakRateEnabled,
		PeakStart:                       req.PeakStart,
		PeakEnd:                         req.PeakEnd,
		PeakRateMultiplier:              req.PeakRateMultiplier,
		PeakTimezone:                    req.PeakTimezone,
		IsExclusive:                     req.IsExclusive,
		LocalOnly:                       req.LocalOnly,
		MinimumTier:                     req.MinimumTier.value,
		MinimumTierSet:                  req.MinimumTier.set,
		SchedulingMode:                  req.SchedulingMode,
		PerUserConcurrency:              req.PerUserConcurrency,
		Status:                          req.Status,
		SubscriptionType:                req.SubscriptionType,
		DailyLimitUSD:                   req.DailyLimitUSD.ToServiceInput(),
		WeeklyLimitUSD:                  req.WeeklyLimitUSD.ToServiceInput(),
		MonthlyLimitUSD:                 req.MonthlyLimitUSD.ToServiceInput(),
		AllowImageGeneration:            req.AllowImageGeneration,
		ProfitControlEnabled:            req.ProfitControlEnabled,
		ProfitMinMargin:                 req.ProfitMinMargin,
		ProfitSafetyBuffer:              req.ProfitSafetyBuffer,
		ImageRateIndependent:            req.ImageRateIndependent,
		ImageRateMultiplier:             req.ImageRateMultiplier,
		ImagePrice1K:                    req.ImagePrice1K.ToServiceInput(),
		ImagePrice2K:                    req.ImagePrice2K.ToServiceInput(),
		ImagePrice4K:                    req.ImagePrice4K.ToServiceInput(),
		VideoRateIndependent:            req.VideoRateIndependent,
		VideoRateMultiplier:             req.VideoRateMultiplier,
		VideoPrice480P:                  req.VideoPrice480P.ToServiceInput(),
		VideoPrice720P:                  req.VideoPrice720P.ToServiceInput(),
		VideoPrice1080P:                 req.VideoPrice1080P.ToServiceInput(),
		VideoModelPrices:                req.VideoModelPrices,
		WebSearchPricePerCall:           req.WebSearchPricePerCall.ToServiceInput(),
		SearchPricePer1k:                req.SearchPricePer1k.ToServiceInput(),
		AudioRealtimePricePerMin:        req.AudioRealtimePricePerMin.ToServiceInput(),
		AudioTTSPricePerMillionChars:    req.AudioTtsPricePerMillionChars.ToServiceInput(),
		AudioSTTPricePerHour:            req.AudioSttPricePerHour.ToServiceInput(),
		AllowOpenAILongContextBilling:   req.AllowOpenAILongContextBilling,
		AllowBlockedKeywordRequests:     req.AllowBlockedKeywordRequests,
		ClaudeCodeOnly:                  req.ClaudeCodeOnly,
		FallbackGroupID:                 req.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: req.FallbackGroupIDOnInvalidRequest,
		FailoverGroupID:                 req.FailoverGroupID,
		FailoverStrategy:                req.FailoverStrategy,
		ModelRouting:                    req.ModelRouting,
		ModelRoutingEnabled:             req.ModelRoutingEnabled,
		ModelMapping:                    req.ModelMapping,
		WeightedModelMapping:            req.WeightedModelMapping,
		FallbackModel:                   req.FallbackModel,
		BillingModelSource:              req.BillingModelSource,
		MCPXMLInject:                    req.MCPXMLInject,
		SupportedModelScopes:            req.SupportedModelScopes,
		AllowMessagesDispatch:           req.AllowMessagesDispatch,
		AllowLive:                       req.AllowLive,
		RequireOAuthOnly:                req.RequireOAuthOnly,
		RequirePrivacySet:               req.RequirePrivacySet,
		DefaultMappedModel:              req.DefaultMappedModel,
		MessagesDispatchModelConfig:     req.MessagesDispatchModelConfig,
		ModelsListConfig:                req.ModelsListConfig,
		RPMLimit:                        req.RPMLimit,
		MaxReasoningEffort:              req.MaxReasoningEffort,
		ReasoningEffortMappings:         req.ReasoningEffortMappings,
		CopyAccountsFromGroupIDs:        req.CopyAccountsFromGroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	reportGatewayLiteConfigChange("groups", "update", groupID)
	response.Success(c, dto.GroupFromServiceAdmin(group))
}

// Delete handles deleting a group
// DELETE /api/v1/admin/groups/:id
func (h *GroupHandler) Delete(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	err = h.adminService.DeleteGroup(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	reportGatewayLiteConfigChange("groups", "delete", groupID)
	response.Success(c, gin.H{"message": "Group deleted successfully"})
}

// GetStats handles getting group statistics
// GET /api/v1/admin/groups/:id/stats
func (h *GroupHandler) GetStats(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	// Return mock data for now
	response.Success(c, gin.H{
		"total_api_keys":  0,
		"active_api_keys": 0,
		"total_requests":  0,
		"total_cost":      0.0,
	})
	_ = groupID // TODO: implement actual stats
}

// GetUsageSummary returns today's and cumulative cost for all groups.
// GET /api/v1/admin/groups/usage-summary?timezone=Asia/Shanghai
func (h *GroupHandler) GetUsageSummary(c *gin.Context) {
	userTZ := c.Query("timezone")
	now := timezone.NowInUserLocation(userTZ)
	todayStart := timezone.StartOfDayInUserLocation(now, userTZ)

	results, err := h.dashboardService.GetGroupUsageSummary(c.Request.Context(), todayStart)
	if err != nil {
		response.Error(c, 500, "Failed to get group usage summary")
		return
	}

	response.Success(c, results)
}

// GetCapacitySummary returns aggregated capacity (concurrency/sessions/RPM) for all active groups.
// GET /api/v1/admin/groups/capacity-summary
func (h *GroupHandler) GetCapacitySummary(c *gin.Context) {
	results, err := h.groupCapacityService.GetAllGroupCapacity(c.Request.Context())
	if err != nil {
		response.Error(c, 500, "Failed to get group capacity summary")
		return
	}
	response.Success(c, results)
}

// GetGroupAPIKeys handles getting API keys in a group
// GET /api/v1/admin/groups/:id/api-keys
func (h *GroupHandler) GetGroupAPIKeys(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	page, pageSize := response.ParsePagination(c)

	keys, total, err := h.adminService.GetGroupAPIKeys(c.Request.Context(), groupID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	outKeys := make([]dto.APIKey, 0, len(keys))
	for i := range keys {
		outKeys = append(outKeys, *dto.APIKeyFromService(&keys[i]))
	}
	response.Paginated(c, outKeys, total, page, pageSize)
}

// GetGroupRateMultipliers handles getting rate multipliers for users in a group
// GET /api/v1/admin/groups/:id/rate-multipliers
func (h *GroupHandler) GetGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	entries, err := h.adminService.GetGroupRateMultipliers(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if entries == nil {
		entries = []service.UserGroupRateEntry{}
	}
	response.Success(c, entries)
}

// ClearGroupRateMultipliers handles clearing all rate multipliers for a group
// DELETE /api/v1/admin/groups/:id/rate-multipliers
func (h *GroupHandler) ClearGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	if err := h.adminService.ClearGroupRateMultipliers(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rate multipliers cleared successfully"})
}

// BatchSetGroupRateMultipliersRequest represents batch set rate multipliers request
type BatchSetGroupRateMultipliersRequest struct {
	Entries []service.GroupRateMultiplierInput `json:"entries" binding:"required"`
}

// BatchSetGroupRateMultipliers handles batch setting rate multipliers for a group
// PUT /api/v1/admin/groups/:id/rate-multipliers
func (h *GroupHandler) BatchSetGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req BatchSetGroupRateMultipliersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.adminService.BatchSetGroupRateMultipliers(c.Request.Context(), groupID, req.Entries); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rate multipliers updated successfully"})
}

// BatchSetGroupRPMOverridesRequest represents batch set rpm_override request
type BatchSetGroupRPMOverridesRequest struct {
	Entries []service.GroupRPMOverrideInput `json:"entries" binding:"required"`
}

// BatchSetGroupRPMOverrides handles batch setting rpm_override for users in a group
// PUT /api/v1/admin/groups/:id/rpm-overrides
func (h *GroupHandler) BatchSetGroupRPMOverrides(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req BatchSetGroupRPMOverridesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.adminService.BatchSetGroupRPMOverrides(c.Request.Context(), groupID, req.Entries); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "RPM overrides updated successfully"})
}

// ClearGroupRPMOverrides handles clearing all rpm_override for a group
// DELETE /api/v1/admin/groups/:id/rpm-overrides
func (h *GroupHandler) ClearGroupRPMOverrides(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	if err := h.adminService.ClearGroupRPMOverrides(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "RPM overrides cleared successfully"})
}

// UpdateSortOrderRequest represents the request to update group sort orders
type UpdateSortOrderRequest struct {
	Updates []struct {
		ID        int64 `json:"id" binding:"required"`
		SortOrder int   `json:"sort_order"`
	} `json:"updates" binding:"required,min=1"`
}

// UpdateSortOrder handles updating group sort orders
// PUT /api/v1/admin/groups/sort-order
func (h *GroupHandler) UpdateSortOrder(c *gin.Context) {
	var req UpdateSortOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	updates := make([]service.GroupSortOrderUpdate, 0, len(req.Updates))
	for _, u := range req.Updates {
		updates = append(updates, service.GroupSortOrderUpdate{
			ID:        u.ID,
			SortOrder: u.SortOrder,
		})
	}

	if err := h.adminService.UpdateGroupSortOrders(c.Request.Context(), updates); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	reportGatewayLiteConfigChange("groups", "sort_order", 0)
	response.Success(c, gin.H{"message": "Sort order updated successfully"})
}
