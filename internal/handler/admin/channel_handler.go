package admin

import (
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ChannelHandler handles admin channel management
type ChannelHandler struct {
	channelService *service.ChannelService
	billingService *service.BillingService
	pricingService *service.PricingService
}

// NewChannelHandler creates a new admin channel handler
func NewChannelHandler(channelService *service.ChannelService, billingService *service.BillingService, pricingService *service.PricingService) *ChannelHandler {
	return &ChannelHandler{channelService: channelService, billingService: billingService, pricingService: pricingService}
}

// --- Request / Response types ---

type createChannelRequest struct {
	Name                       string                           `json:"name" binding:"required,max=100"`
	Description                string                           `json:"description"`
	GroupIDs                   []int64                          `json:"group_ids"`
	ModelPricing               []channelModelPricingRequest     `json:"model_pricing"`
	ModelMapping               map[string]map[string]string     `json:"model_mapping"`
	BillingModelSource         string                           `json:"billing_model_source" binding:"omitempty,oneof=requested upstream channel_mapped response_model"`
	RestrictModels             bool                             `json:"restrict_models"`
	Features                   string                           `json:"features"`
	FeaturesConfig             map[string]any                   `json:"features_config"`
	ApplyPricingToAccountStats bool                             `json:"apply_pricing_to_account_stats"`
	AccountStatsPricingRules   []accountStatsPricingRuleRequest `json:"account_stats_pricing_rules"`
}

type updateChannelRequest struct {
	Name                       string                            `json:"name" binding:"omitempty,max=100"`
	Description                *string                           `json:"description"`
	Status                     string                            `json:"status" binding:"omitempty,oneof=active disabled"`
	GroupIDs                   *[]int64                          `json:"group_ids"`
	ModelPricing               *[]channelModelPricingRequest     `json:"model_pricing"`
	ModelMapping               map[string]map[string]string      `json:"model_mapping"`
	BillingModelSource         string                            `json:"billing_model_source" binding:"omitempty,oneof=requested upstream channel_mapped response_model"`
	RestrictModels             *bool                             `json:"restrict_models"`
	Features                   *string                           `json:"features"`
	FeaturesConfig             map[string]any                    `json:"features_config"`
	ApplyPricingToAccountStats *bool                             `json:"apply_pricing_to_account_stats"`
	AccountStatsPricingRules   *[]accountStatsPricingRuleRequest `json:"account_stats_pricing_rules"`
}

type channelModelPricingRequest struct {
	Platform         string                   `json:"platform" binding:"omitempty,max=50"`
	Models           []string                 `json:"models" binding:"required,min=1,max=100"`
	BillingMode      string                   `json:"billing_mode" binding:"omitempty,oneof=token per_request image video"`
	InputPrice       *float64                 `json:"input_price" binding:"omitempty,min=0"`
	OutputPrice      *float64                 `json:"output_price" binding:"omitempty,min=0"`
	CacheWritePrice  *float64                 `json:"cache_write_price" binding:"omitempty,min=0"`
	CacheReadPrice   *float64                 `json:"cache_read_price" binding:"omitempty,min=0"`
	FastMultiplier   *float64                 `json:"fast_multiplier" binding:"omitempty,gt=0"`
	FlexMultiplier   *float64                 `json:"flex_multiplier" binding:"omitempty,gt=0"`
	ImageInputPrice  *float64                 `json:"image_input_price" binding:"omitempty,min=0"`
	ImageOutputPrice *float64                 `json:"image_output_price" binding:"omitempty,min=0"`
	PerRequestPrice  *float64                 `json:"per_request_price" binding:"omitempty,min=0"`
	Intervals        []pricingIntervalRequest `json:"intervals"`
}

type pricingIntervalRequest struct {
	MinTokens            int      `json:"min_tokens"`
	MaxTokens            *int     `json:"max_tokens"`
	TierLabel            string   `json:"tier_label"`
	InputPrice           *float64 `json:"input_price"`
	OutputPrice          *float64 `json:"output_price"`
	CacheWritePrice      *float64 `json:"cache_write_price"`
	CacheReadPrice       *float64 `json:"cache_read_price"`
	InputMultiplier      *float64 `json:"input_multiplier" binding:"omitempty,gt=0"`
	OutputMultiplier     *float64 `json:"output_multiplier" binding:"omitempty,gt=0"`
	CacheWriteMultiplier *float64 `json:"cache_write_multiplier" binding:"omitempty,gt=0"`
	CacheReadMultiplier  *float64 `json:"cache_read_multiplier" binding:"omitempty,gt=0"`
	PerRequestPrice      *float64 `json:"per_request_price"`
	SortOrder            int      `json:"sort_order"`
}

type accountStatsPricingRuleRequest struct {
	Name       string                       `json:"name"`
	GroupIDs   []int64                      `json:"group_ids"`
	AccountIDs []int64                      `json:"account_ids"`
	Pricing    []channelModelPricingRequest `json:"pricing"`
}

type channelResponse struct {
	ID                         int64                             `json:"id"`
	Name                       string                            `json:"name"`
	Description                string                            `json:"description"`
	Status                     string                            `json:"status"`
	BillingModelSource         string                            `json:"billing_model_source"`
	RestrictModels             bool                              `json:"restrict_models"`
	Features                   string                            `json:"features"`
	FeaturesConfig             map[string]any                    `json:"features_config"`
	GroupIDs                   []int64                           `json:"group_ids"`
	ModelPricing               []channelModelPricingResponse     `json:"model_pricing"`
	ModelMapping               map[string]map[string]string      `json:"model_mapping"`
	ApplyPricingToAccountStats bool                              `json:"apply_pricing_to_account_stats"`
	AccountStatsPricingRules   []accountStatsPricingRuleResponse `json:"account_stats_pricing_rules"`
	CreatedAt                  string                            `json:"created_at"`
	UpdatedAt                  string                            `json:"updated_at"`
}

type channelModelPricingResponse struct {
	ID               int64                     `json:"id"`
	Platform         string                    `json:"platform"`
	Models           []string                  `json:"models"`
	BillingMode      string                    `json:"billing_mode"`
	InputPrice       *float64                  `json:"input_price"`
	OutputPrice      *float64                  `json:"output_price"`
	CacheWritePrice  *float64                  `json:"cache_write_price"`
	CacheReadPrice   *float64                  `json:"cache_read_price"`
	FastMultiplier   *float64                  `json:"fast_multiplier"`
	FlexMultiplier   *float64                  `json:"flex_multiplier"`
	ImageInputPrice  *float64                  `json:"image_input_price"`
	ImageOutputPrice *float64                  `json:"image_output_price"`
	PerRequestPrice  *float64                  `json:"per_request_price"`
	Intervals        []pricingIntervalResponse `json:"intervals"`
}

type pricingIntervalResponse struct {
	ID                   int64    `json:"id"`
	MinTokens            int      `json:"min_tokens"`
	MaxTokens            *int     `json:"max_tokens"`
	TierLabel            string   `json:"tier_label,omitempty"`
	InputPrice           *float64 `json:"input_price"`
	OutputPrice          *float64 `json:"output_price"`
	CacheWritePrice      *float64 `json:"cache_write_price"`
	CacheReadPrice       *float64 `json:"cache_read_price"`
	InputMultiplier      *float64 `json:"input_multiplier"`
	OutputMultiplier     *float64 `json:"output_multiplier"`
	CacheWriteMultiplier *float64 `json:"cache_write_multiplier"`
	CacheReadMultiplier  *float64 `json:"cache_read_multiplier"`
	PerRequestPrice      *float64 `json:"per_request_price"`
	SortOrder            int      `json:"sort_order"`
}

type accountStatsPricingRuleResponse struct {
	ID         int64                         `json:"id"`
	Name       string                        `json:"name"`
	GroupIDs   []int64                       `json:"group_ids"`
	AccountIDs []int64                       `json:"account_ids"`
	Pricing    []channelModelPricingResponse `json:"pricing"`
}

type modelPriceCatalogResponse struct {
	Model                                         string   `json:"model"`
	Provider                                      string   `json:"provider"`
	ModelVendor                                   string   `json:"model_vendor,omitempty"`
	InputModalities                               []string `json:"input_modalities,omitempty"`
	MaxInputTokens                                int      `json:"max_input_tokens,omitempty"`
	MetadataSource                                string   `json:"metadata_source,omitempty"`
	PriceFields                                   []string `json:"price_fields"`
	Mode                                          string   `json:"mode"`
	BillingMode                                   string   `json:"billing_mode"`
	InputCostPer1MTokens                          float64  `json:"input_cost_per_1m_tokens"`
	InputCostPer1MTokensPriority                  float64  `json:"input_cost_per_1m_tokens_priority,omitempty"`
	OutputCostPer1MTokens                         float64  `json:"output_cost_per_1m_tokens"`
	OutputCostPer1MTokensPriority                 float64  `json:"output_cost_per_1m_tokens_priority,omitempty"`
	CacheCreationCostPer1MTokens                  float64  `json:"cache_creation_cost_per_1m_tokens"`
	CacheCreationCostPer1MTokensPriority          float64  `json:"cache_creation_cost_per_1m_tokens_priority,omitempty"`
	CacheReadCostPer1MTokens                      float64  `json:"cache_read_cost_per_1m_tokens"`
	CacheReadCostPer1MTokensPriority              float64  `json:"cache_read_cost_per_1m_tokens_priority,omitempty"`
	InputCostPer1MTokensAbove272K                 float64  `json:"input_cost_per_1m_tokens_above_272k,omitempty"`
	InputCostPer1MTokensPriorityAbove272K         float64  `json:"input_cost_per_1m_tokens_priority_above_272k,omitempty"`
	OutputCostPer1MTokensAbove272K                float64  `json:"output_cost_per_1m_tokens_above_272k,omitempty"`
	OutputCostPer1MTokensPriorityAbove272K        float64  `json:"output_cost_per_1m_tokens_priority_above_272k,omitempty"`
	CacheCreationCostPer1MTokensAbove272K         float64  `json:"cache_creation_cost_per_1m_tokens_above_272k,omitempty"`
	CacheCreationCostPer1MTokensPriorityAbove272K float64  `json:"cache_creation_cost_per_1m_tokens_priority_above_272k,omitempty"`
	CacheReadCostPer1MTokensAbove272K             float64  `json:"cache_read_cost_per_1m_tokens_above_272k,omitempty"`
	CacheReadCostPer1MTokensPriorityAbove272K     float64  `json:"cache_read_cost_per_1m_tokens_priority_above_272k,omitempty"`
	LongContextInputTokenThreshold                int      `json:"long_context_input_token_threshold,omitempty"`
	OutputCostPerImage                            float64  `json:"output_cost_per_image"`
	OutputCostPerImageToken                       float64  `json:"output_cost_per_image_token"`
	InputCostPerImageToken                        *float64 `json:"input_cost_per_image_token"`
	PerRequestPrice                               float64  `json:"per_request_price"`
	VideoPricePerSecond480P                       float64  `json:"video_price_per_second_480p,omitempty"`
	VideoPricePerSecond720P                       float64  `json:"video_price_per_second_720p,omitempty"`
	VideoPricePerSecond1080P                      float64  `json:"video_price_per_second_1080p,omitempty"`
	Currency                                      string   `json:"currency"`
	SupportsPromptCaching                         bool     `json:"supports_prompt_caching"`
	SupportsServiceTier                           bool     `json:"supports_service_tier"`
	Source                                        string   `json:"source"`
	IsOverride                                    bool     `json:"is_override"`
	Disabled                                      bool     `json:"disabled"`
}

type upsertModelPriceRequest struct {
	Model                                         string   `json:"model" binding:"required,max=200"`
	Provider                                      string   `json:"provider" binding:"omitempty,max=80"`
	Mode                                          string   `json:"mode" binding:"omitempty,max=40"`
	BillingMode                                   string   `json:"billing_mode" binding:"omitempty,oneof=token per_request image video"`
	InputCostPer1MTokens                          *float64 `json:"input_cost_per_1m_tokens" binding:"omitempty,min=0"`
	InputCostPer1MTokensPriority                  *float64 `json:"input_cost_per_1m_tokens_priority" binding:"omitempty,min=0"`
	OutputCostPer1MTokens                         *float64 `json:"output_cost_per_1m_tokens" binding:"omitempty,min=0"`
	OutputCostPer1MTokensPriority                 *float64 `json:"output_cost_per_1m_tokens_priority" binding:"omitempty,min=0"`
	CacheCreationCostPer1MTokens                  *float64 `json:"cache_creation_cost_per_1m_tokens" binding:"omitempty,min=0"`
	CacheCreationCostPer1MTokensPriority          *float64 `json:"cache_creation_cost_per_1m_tokens_priority" binding:"omitempty,min=0"`
	CacheReadCostPer1MTokens                      *float64 `json:"cache_read_cost_per_1m_tokens" binding:"omitempty,min=0"`
	CacheReadCostPer1MTokensPriority              *float64 `json:"cache_read_cost_per_1m_tokens_priority" binding:"omitempty,min=0"`
	InputCostPer1MTokensAbove272K                 *float64 `json:"input_cost_per_1m_tokens_above_272k" binding:"omitempty,min=0"`
	InputCostPer1MTokensPriorityAbove272K         *float64 `json:"input_cost_per_1m_tokens_priority_above_272k" binding:"omitempty,min=0"`
	OutputCostPer1MTokensAbove272K                *float64 `json:"output_cost_per_1m_tokens_above_272k" binding:"omitempty,min=0"`
	OutputCostPer1MTokensPriorityAbove272K        *float64 `json:"output_cost_per_1m_tokens_priority_above_272k" binding:"omitempty,min=0"`
	CacheCreationCostPer1MTokensAbove272K         *float64 `json:"cache_creation_cost_per_1m_tokens_above_272k" binding:"omitempty,min=0"`
	CacheCreationCostPer1MTokensPriorityAbove272K *float64 `json:"cache_creation_cost_per_1m_tokens_priority_above_272k" binding:"omitempty,min=0"`
	CacheReadCostPer1MTokensAbove272K             *float64 `json:"cache_read_cost_per_1m_tokens_above_272k" binding:"omitempty,min=0"`
	CacheReadCostPer1MTokensPriorityAbove272K     *float64 `json:"cache_read_cost_per_1m_tokens_priority_above_272k" binding:"omitempty,min=0"`
	LongContextInputTokenThreshold                *int     `json:"long_context_input_token_threshold" binding:"omitempty,min=0"`
	OutputCostPerImage                            *float64 `json:"output_cost_per_image" binding:"omitempty,min=0"`
	OutputCostPerImageToken                       *float64 `json:"output_cost_per_image_token" binding:"omitempty,min=0"`
	InputCostPerImageToken                        *float64 `json:"input_cost_per_image_token" binding:"omitempty,min=0"`
	PerRequestPrice                               *float64 `json:"per_request_price" binding:"omitempty,min=0"`
	VideoPricePerSecond480P                       *float64 `json:"video_price_per_second_480p" binding:"omitempty,min=0"`
	VideoPricePerSecond720P                       *float64 `json:"video_price_per_second_720p" binding:"omitempty,min=0"`
	VideoPricePerSecond1080P                      *float64 `json:"video_price_per_second_1080p" binding:"omitempty,min=0"`
	SupportsPromptCaching                         bool     `json:"supports_prompt_caching"`
	SupportsServiceTier                           bool     `json:"supports_service_tier"`
	Disabled                                      bool     `json:"disabled"`
}

func channelToResponse(ch *service.Channel) *channelResponse {
	if ch == nil {
		return nil
	}
	resp := &channelResponse{
		ID:             ch.ID,
		Name:           ch.Name,
		Description:    ch.Description,
		Status:         ch.Status,
		RestrictModels: ch.RestrictModels,
		Features:       ch.Features,
		FeaturesConfig: ch.FeaturesConfig,
		GroupIDs:       ch.GroupIDs,
		ModelMapping:   ch.ModelMapping,
		CreatedAt:      ch.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:      ch.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
	resp.BillingModelSource = ch.BillingModelSource
	if resp.GroupIDs == nil {
		resp.GroupIDs = []int64{}
	}
	if resp.ModelMapping == nil {
		resp.ModelMapping = map[string]map[string]string{}
	}

	resp.ModelPricing = make([]channelModelPricingResponse, 0, len(ch.ModelPricing))
	for _, p := range ch.ModelPricing {
		resp.ModelPricing = append(resp.ModelPricing, pricingToResponse(&p))
	}

	resp.ApplyPricingToAccountStats = ch.ApplyPricingToAccountStats
	resp.AccountStatsPricingRules = make([]accountStatsPricingRuleResponse, 0, len(ch.AccountStatsPricingRules))
	for _, rule := range ch.AccountStatsPricingRules {
		ruleResp := accountStatsPricingRuleResponse{
			ID:         rule.ID,
			Name:       rule.Name,
			GroupIDs:   rule.GroupIDs,
			AccountIDs: rule.AccountIDs,
			Pricing:    make([]channelModelPricingResponse, 0, len(rule.Pricing)),
		}
		if ruleResp.GroupIDs == nil {
			ruleResp.GroupIDs = []int64{}
		}
		if ruleResp.AccountIDs == nil {
			ruleResp.AccountIDs = []int64{}
		}
		for i := range rule.Pricing {
			ruleResp.Pricing = append(ruleResp.Pricing, pricingToResponse(&rule.Pricing[i]))
		}
		resp.AccountStatsPricingRules = append(resp.AccountStatsPricingRules, ruleResp)
	}

	return resp
}

func pricingToResponse(p *service.ChannelModelPricing) channelModelPricingResponse {
	models := p.Models
	if models == nil {
		models = []string{}
	}
	billingMode := string(p.BillingMode)
	if billingMode == "" {
		billingMode = string(service.BillingModeToken)
	}
	platform := p.Platform
	if platform == "" {
		platform = service.PlatformAnthropic
	}
	intervals := make([]pricingIntervalResponse, 0, len(p.Intervals))
	for _, iv := range p.Intervals {
		intervals = append(intervals, intervalToResponse(iv))
	}
	return channelModelPricingResponse{
		ID:               p.ID,
		Platform:         platform,
		Models:           models,
		BillingMode:      billingMode,
		InputPrice:       p.InputPrice,
		OutputPrice:      p.OutputPrice,
		CacheWritePrice:  p.CacheWritePrice,
		CacheReadPrice:   p.CacheReadPrice,
		FastMultiplier:   p.FastMultiplier,
		FlexMultiplier:   p.FlexMultiplier,
		ImageInputPrice:  p.ImageInputPrice,
		ImageOutputPrice: p.ImageOutputPrice,
		PerRequestPrice:  p.PerRequestPrice,
		Intervals:        intervals,
	}
}

func intervalToResponse(iv service.PricingInterval) pricingIntervalResponse {
	return pricingIntervalResponse{
		ID:                   iv.ID,
		MinTokens:            iv.MinTokens,
		MaxTokens:            iv.MaxTokens,
		TierLabel:            iv.TierLabel,
		InputPrice:           iv.InputPrice,
		OutputPrice:          iv.OutputPrice,
		CacheWritePrice:      iv.CacheWritePrice,
		CacheReadPrice:       iv.CacheReadPrice,
		InputMultiplier:      iv.InputMultiplier,
		OutputMultiplier:     iv.OutputMultiplier,
		CacheWriteMultiplier: iv.CacheWriteMultiplier,
		CacheReadMultiplier:  iv.CacheReadMultiplier,
		PerRequestPrice:      iv.PerRequestPrice,
		SortOrder:            iv.SortOrder,
	}
}

func pricingRequestToService(reqs []channelModelPricingRequest, allowChannelMultipliers ...bool) []service.ChannelModelPricing {
	allowMultipliers := len(allowChannelMultipliers) == 0 || allowChannelMultipliers[0]
	result := make([]service.ChannelModelPricing, 0, len(reqs))
	for _, r := range reqs {
		billingMode := service.BillingMode(r.BillingMode)
		if billingMode == "" {
			billingMode = service.BillingModeToken
		}
		platform := r.Platform
		intervals := make([]service.PricingInterval, 0, len(r.Intervals))
		for _, iv := range r.Intervals {
			converted := service.PricingInterval{
				MinTokens:       iv.MinTokens,
				MaxTokens:       iv.MaxTokens,
				TierLabel:       iv.TierLabel,
				InputPrice:      iv.InputPrice,
				OutputPrice:     iv.OutputPrice,
				CacheWritePrice: iv.CacheWritePrice,
				CacheReadPrice:  iv.CacheReadPrice,
				PerRequestPrice: iv.PerRequestPrice,
				SortOrder:       iv.SortOrder,
			}
			if allowMultipliers {
				converted.InputMultiplier = iv.InputMultiplier
				converted.OutputMultiplier = iv.OutputMultiplier
				converted.CacheWriteMultiplier = iv.CacheWriteMultiplier
				converted.CacheReadMultiplier = iv.CacheReadMultiplier
			}
			intervals = append(intervals, converted)
		}
		converted := service.ChannelModelPricing{
			Platform:         platform,
			Models:           r.Models,
			BillingMode:      billingMode,
			InputPrice:       r.InputPrice,
			OutputPrice:      r.OutputPrice,
			CacheWritePrice:  r.CacheWritePrice,
			CacheReadPrice:   r.CacheReadPrice,
			ImageInputPrice:  r.ImageInputPrice,
			ImageOutputPrice: r.ImageOutputPrice,
			PerRequestPrice:  r.PerRequestPrice,
			Intervals:        intervals,
		}
		if allowMultipliers {
			converted.FastMultiplier = r.FastMultiplier
			converted.FlexMultiplier = r.FlexMultiplier
		}
		result = append(result, converted)
	}
	return result
}

func accountStatsPricingRuleRequestToService(r accountStatsPricingRuleRequest) service.AccountStatsPricingRule {
	return service.AccountStatsPricingRule{
		Name:       r.Name,
		GroupIDs:   r.GroupIDs,
		AccountIDs: r.AccountIDs,
		Pricing:    pricingRequestToService(r.Pricing, false),
	}
}

// --- Handlers ---

// List handles listing channels with pagination
// GET /api/v1/admin/channels
func (h *ChannelHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	status := c.Query("status")
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 100 {
		search = search[:100]
	}

	channels, pag, err := h.channelService.List(c.Request.Context(), pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}, status, search)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]*channelResponse, 0, len(channels))
	for i := range channels {
		out = append(out, channelToResponse(&channels[i]))
	}
	response.Paginated(c, out, pag.Total, page, pageSize)
}

// GetByID handles getting a channel by ID
// GET /api/v1/admin/channels/:id
func (h *ChannelHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_CHANNEL_ID", "Invalid channel ID"))
		return
	}

	channel, err := h.channelService.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// 渠道价格是请求热路径的最终覆盖层；创建后主动刷新主站价格镜像，
	// 避免主站继续展示创建前的继承价格。
	reportGatewayLiteConfigChange("model_prices", "channel_create", channel.ID)
	response.Success(c, channelToResponse(channel))
}

// Create handles creating a new channel
// POST /api/v1/admin/channels
func (h *ChannelHandler) Create(c *gin.Context) {
	var req createChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}

	pricing := pricingRequestToService(req.ModelPricing)
	// Main model_pricing requires a platform; default to anthropic for backward compatibility.
	for i := range pricing {
		if pricing[i].Platform == "" {
			pricing[i].Platform = service.PlatformAnthropic
		}
	}

	var statsRules []service.AccountStatsPricingRule
	for i, r := range req.AccountStatsPricingRules {
		if len(r.GroupIDs) == 0 && len(r.AccountIDs) == 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("PRICING_RULE_EMPTY_SCOPE",
				fmt.Sprintf("pricing rule #%d must have at least one group or account", i+1)))
			return
		}
		if len(r.Pricing) == 0 {
			response.ErrorFrom(c, infraerrors.BadRequest("PRICING_RULE_EMPTY_PRICING",
				fmt.Sprintf("pricing rule #%d must have at least one pricing entry", i+1)))
			return
		}
		rule := accountStatsPricingRuleRequestToService(r)
		rule.SortOrder = i
		statsRules = append(statsRules, rule)
	}

	channel, err := h.channelService.Create(c.Request.Context(), &service.CreateChannelInput{
		Name:                       req.Name,
		Description:                req.Description,
		GroupIDs:                   req.GroupIDs,
		ModelPricing:               pricing,
		ModelMapping:               req.ModelMapping,
		BillingModelSource:         req.BillingModelSource,
		RestrictModels:             req.RestrictModels,
		Features:                   req.Features,
		FeaturesConfig:             req.FeaturesConfig,
		ApplyPricingToAccountStats: req.ApplyPricingToAccountStats,
		AccountStatsPricingRules:   statsRules,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// 渠道价格是请求热路径的最终覆盖层；创建后主动刷新主站价格镜像，
	// 避免主站继续展示创建前的继承价格。
	reportGatewayLiteConfigChange("model_prices", "channel_create", channel.ID)
	response.Success(c, channelToResponse(channel))
}

// Update handles updating a channel
// PUT /api/v1/admin/channels/:id
func (h *ChannelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_CHANNEL_ID", "Invalid channel ID"))
		return
	}

	var req updateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}

	input := &service.UpdateChannelInput{
		Name:                       req.Name,
		Description:                req.Description,
		Status:                     req.Status,
		GroupIDs:                   req.GroupIDs,
		ModelMapping:               req.ModelMapping,
		BillingModelSource:         req.BillingModelSource,
		RestrictModels:             req.RestrictModels,
		Features:                   req.Features,
		FeaturesConfig:             req.FeaturesConfig,
		ApplyPricingToAccountStats: req.ApplyPricingToAccountStats,
	}
	if req.ModelPricing != nil {
		pricing := pricingRequestToService(*req.ModelPricing)
		for i := range pricing {
			if pricing[i].Platform == "" {
				pricing[i].Platform = service.PlatformAnthropic
			}
		}
		input.ModelPricing = &pricing
	}
	if req.AccountStatsPricingRules != nil {
		statsRules := make([]service.AccountStatsPricingRule, 0, len(*req.AccountStatsPricingRules))
		for i, r := range *req.AccountStatsPricingRules {
			if len(r.GroupIDs) == 0 && len(r.AccountIDs) == 0 {
				response.ErrorFrom(c, infraerrors.BadRequest("PRICING_RULE_EMPTY_SCOPE",
					fmt.Sprintf("pricing rule #%d must have at least one group or account", i+1)))
				return
			}
			if len(r.Pricing) == 0 {
				response.ErrorFrom(c, infraerrors.BadRequest("PRICING_RULE_EMPTY_PRICING",
					fmt.Sprintf("pricing rule #%d must have at least one pricing entry", i+1)))
				return
			}
			rule := accountStatsPricingRuleRequestToService(r)
			rule.SortOrder = i
			statsRules = append(statsRules, rule)
		}
		input.AccountStatsPricingRules = &statsRules
	}

	channel, err := h.channelService.Update(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if req.ModelPricing != nil {
		// 包含空数组的更新表示清空渠道覆盖价，也必须触发镜像刷新；
		// 不能只在非空价格时通知，否则主站会保留陈旧的图片输入价。
		reportGatewayLiteConfigChange("model_prices", "channel_update", channel.ID)
	}
	response.Success(c, channelToResponse(channel))
}

// Delete handles deleting a channel
// DELETE /api/v1/admin/channels/:id
func (h *ChannelHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("INVALID_CHANNEL_ID", "Invalid channel ID"))
		return
	}

	if err := h.channelService.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	reportGatewayLiteConfigChange("model_prices", "channel_delete", id)
	response.Success(c, gin.H{"message": "Channel deleted successfully"})
}

// ListModelPrices 返回当前生效的模型价格目录。
// GET /api/v1/admin/model-prices
func (h *ChannelHandler) ListModelPrices(c *gin.Context) {
	if h.pricingService == nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PRICING_SERVICE_UNAVAILABLE", "pricing service is unavailable"))
		return
	}

	page, pageSize := response.ParsePagination(c)
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	source := strings.ToLower(strings.TrimSpace(c.Query("source")))

	all := h.pricingService.ListModelPricingCatalog()
	filtered := make([]modelPriceCatalogResponse, 0, len(all))
	for _, entry := range all {
		metadata := h.pricingService.GetModelPricingDisplayMetadata(entry.Model)
		if metadata != nil {
			entry.ModelVendor = metadata.ModelVendor
			entry.InputModalities = metadata.InputModalities
			entry.MaxInputTokens = metadata.MaxInputTokens
			entry.MetadataSource = metadata.MetadataSource
		}
		resp := modelPriceCatalogEntryToResponse(entry)
		if search != "" &&
			!strings.Contains(strings.ToLower(resp.Model), search) &&
			!strings.Contains(strings.ToLower(resp.Provider), search) {
			continue
		}
		if provider != "" && strings.ToLower(resp.Provider) != provider {
			continue
		}
		if source != "" && strings.ToLower(resp.Source) != source {
			continue
		}
		filtered = append(filtered, resp)
	}

	total := int64(len(filtered))
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	response.Paginated(c, filtered[start:end], total, page, pageSize)
}

// GetModelPriceSyncStatus 返回公开基础价格目录的同步状态，不包含本地凭据。
// GET /api/v1/admin/model-prices/sync/status
func (h *ChannelHandler) GetModelPriceSyncStatus(c *gin.Context) {
	if h.pricingService == nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PRICING_SERVICE_UNAVAILABLE", "pricing service is unavailable"))
		return
	}
	response.Success(c, h.pricingService.GetStatus())
}

// SyncModelPrices 立即拉取并完整校验公开基础价格目录。
// POST /api/v1/admin/model-prices/sync
func (h *ChannelHandler) SyncModelPrices(c *gin.Context) {
	if h.pricingService == nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PRICING_SERVICE_UNAVAILABLE", "pricing service is unavailable"))
		return
	}
	if err := h.pricingService.ForceUpdate(); err != nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("MODEL_PRICE_SYNC_FAILED", "模型价格目录同步失败："+err.Error()))
		return
	}
	reportGatewayLiteConfigChange("model_prices", "remote_sync", 0)
	response.Success(c, h.pricingService.GetStatus())
}

// UpsertModelPrice 保存模型价格覆盖配置。
// PUT /api/v1/admin/model-prices
func (h *ChannelHandler) UpsertModelPrice(c *gin.Context) {
	if h.pricingService == nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PRICING_SERVICE_UNAVAILABLE", "pricing service is unavailable"))
		return
	}

	var req upsertModelPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("VALIDATION_ERROR", err.Error()))
		return
	}
	if req.InputCostPer1MTokens == nil &&
		req.InputCostPer1MTokensPriority == nil &&
		req.OutputCostPer1MTokens == nil &&
		req.OutputCostPer1MTokensPriority == nil &&
		req.CacheCreationCostPer1MTokens == nil &&
		req.CacheCreationCostPer1MTokensPriority == nil &&
		req.CacheReadCostPer1MTokens == nil &&
		req.CacheReadCostPer1MTokensPriority == nil &&
		req.InputCostPer1MTokensAbove272K == nil &&
		req.InputCostPer1MTokensPriorityAbove272K == nil &&
		req.OutputCostPer1MTokensAbove272K == nil &&
		req.OutputCostPer1MTokensPriorityAbove272K == nil &&
		req.CacheCreationCostPer1MTokensAbove272K == nil &&
		req.CacheCreationCostPer1MTokensPriorityAbove272K == nil &&
		req.CacheReadCostPer1MTokensAbove272K == nil &&
		req.CacheReadCostPer1MTokensPriorityAbove272K == nil &&
		req.OutputCostPerImage == nil &&
		req.OutputCostPerImageToken == nil &&
		req.InputCostPerImageToken == nil &&
		req.PerRequestPrice == nil &&
		req.VideoPricePerSecond480P == nil &&
		req.VideoPricePerSecond720P == nil &&
		req.VideoPricePerSecond1080P == nil &&
		!req.Disabled {
		response.ErrorFrom(c, infraerrors.BadRequest("MODEL_PRICE_EMPTY", "at least one price field is required"))
		return
	}

	pricing := service.LiteLLMModelPricing{
		PriceFields:           modelPriceRequestDisplayFields(req),
		LiteLLMProvider:       strings.TrimSpace(req.Provider),
		Mode:                  strings.TrimSpace(req.Mode),
		BillingMode:           strings.TrimSpace(req.BillingMode),
		SupportsPromptCaching: req.SupportsPromptCaching,
		SupportsServiceTier:   req.SupportsServiceTier,
		Disabled:              req.Disabled,
	}
	if req.InputCostPer1MTokens != nil {
		pricing.InputCostPerToken = perMillionToPerToken(*req.InputCostPer1MTokens)
	}
	if req.InputCostPer1MTokensPriority != nil {
		pricing.InputCostPerTokenPriority = perMillionToPerToken(*req.InputCostPer1MTokensPriority)
	}
	if req.OutputCostPer1MTokens != nil {
		pricing.OutputCostPerToken = perMillionToPerToken(*req.OutputCostPer1MTokens)
	}
	if req.OutputCostPer1MTokensPriority != nil {
		pricing.OutputCostPerTokenPriority = perMillionToPerToken(*req.OutputCostPer1MTokensPriority)
	}
	if req.CacheCreationCostPer1MTokens != nil {
		pricing.CacheCreationInputTokenCost = perMillionToPerToken(*req.CacheCreationCostPer1MTokens)
	}
	if req.CacheCreationCostPer1MTokensPriority != nil {
		pricing.CacheCreationInputTokenCostPriority = perMillionToPerToken(*req.CacheCreationCostPer1MTokensPriority)
	}
	if req.CacheReadCostPer1MTokens != nil {
		pricing.CacheReadInputTokenCost = perMillionToPerToken(*req.CacheReadCostPer1MTokens)
	}
	if req.CacheReadCostPer1MTokensPriority != nil {
		pricing.CacheReadInputTokenCostPriority = perMillionToPerToken(*req.CacheReadCostPer1MTokensPriority)
	}
	if req.InputCostPer1MTokensAbove272K != nil {
		pricing.InputCostPerTokenAbove272K = perMillionToPerToken(*req.InputCostPer1MTokensAbove272K)
	}
	if req.InputCostPer1MTokensPriorityAbove272K != nil {
		pricing.InputCostPerTokenPriorityAbove272K = perMillionToPerToken(*req.InputCostPer1MTokensPriorityAbove272K)
	}
	if req.OutputCostPer1MTokensAbove272K != nil {
		pricing.OutputCostPerTokenAbove272K = perMillionToPerToken(*req.OutputCostPer1MTokensAbove272K)
	}
	if req.OutputCostPer1MTokensPriorityAbove272K != nil {
		pricing.OutputCostPerTokenPriorityAbove272K = perMillionToPerToken(*req.OutputCostPer1MTokensPriorityAbove272K)
	}
	if req.CacheCreationCostPer1MTokensAbove272K != nil {
		pricing.CacheCreationInputTokenCostAbove272K = perMillionToPerToken(*req.CacheCreationCostPer1MTokensAbove272K)
	}
	if req.CacheCreationCostPer1MTokensPriorityAbove272K != nil {
		pricing.CacheCreationInputTokenCostPriorityAbove272K = perMillionToPerToken(*req.CacheCreationCostPer1MTokensPriorityAbove272K)
	}
	if req.CacheReadCostPer1MTokensAbove272K != nil {
		pricing.CacheReadInputTokenCostAbove272K = perMillionToPerToken(*req.CacheReadCostPer1MTokensAbove272K)
	}
	if req.CacheReadCostPer1MTokensPriorityAbove272K != nil {
		pricing.CacheReadInputTokenCostPriorityAbove272K = perMillionToPerToken(*req.CacheReadCostPer1MTokensPriorityAbove272K)
	}
	if req.LongContextInputTokenThreshold != nil {
		pricing.LongContextInputTokenThreshold = *req.LongContextInputTokenThreshold
	}
	if pricing.LongContextInputTokenThreshold > 0 {
		if pricing.InputCostPerToken > 0 && pricing.InputCostPerTokenAbove272K > 0 {
			pricing.LongContextInputCostMultiplier = pricing.InputCostPerTokenAbove272K / pricing.InputCostPerToken
		}
		if pricing.OutputCostPerToken > 0 && pricing.OutputCostPerTokenAbove272K > 0 {
			pricing.LongContextOutputCostMultiplier = pricing.OutputCostPerTokenAbove272K / pricing.OutputCostPerToken
		}
	}
	if req.OutputCostPerImage != nil {
		pricing.OutputCostPerImage = *req.OutputCostPerImage
	}
	if req.OutputCostPerImageToken != nil {
		pricing.OutputCostPerImageToken = *req.OutputCostPerImageToken
	}
	if req.InputCostPerImageToken != nil {
		value := *req.InputCostPerImageToken
		pricing.InputCostPerImageToken = &value
	}
	if req.PerRequestPrice != nil {
		pricing.PerRequestPrice = *req.PerRequestPrice
	}
	if req.VideoPricePerSecond480P != nil {
		pricing.VideoPricePerSecond480P = *req.VideoPricePerSecond480P
	}
	if req.VideoPricePerSecond720P != nil {
		pricing.VideoPricePerSecond720P = *req.VideoPricePerSecond720P
	}
	if req.VideoPricePerSecond1080P != nil {
		pricing.VideoPricePerSecond1080P = *req.VideoPricePerSecond1080P
	}

	entry, err := h.pricingService.UpsertModelPricingOverride(req.Model, pricing)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("MODEL_PRICE_SAVE_FAILED", err.Error()))
		return
	}
	reportGatewayLiteConfigChange("model_prices", "upsert", 0)
	response.Success(c, modelPriceCatalogEntryToResponse(entry))
}

// DeleteModelPrice 删除覆盖价，生效价格回退到默认目录。
// DELETE /api/v1/admin/model-prices?model=mimo-v2.5
func (h *ChannelHandler) DeleteModelPrice(c *gin.Context) {
	if h.pricingService == nil {
		response.ErrorFrom(c, infraerrors.InternalServer("PRICING_SERVICE_UNAVAILABLE", "pricing service is unavailable"))
		return
	}
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("MISSING_PARAMETER", "model parameter is required"))
		return
	}
	entry, deleted, err := h.pricingService.DeleteModelPricingOverride(model)
	if err != nil {
		response.ErrorFrom(c, infraerrors.BadRequest("MODEL_PRICE_DELETE_FAILED", err.Error()))
		return
	}
	if deleted {
		reportGatewayLiteConfigChange("model_prices", "delete", 0)
	}
	response.Success(c, gin.H{
		"deleted": deleted,
		"current": modelPriceCatalogEntryToResponse(entry),
	})
}

func modelPriceCatalogEntryToResponse(entry service.ModelPricingCatalogEntry) modelPriceCatalogResponse {
	// 精确元数据合并后仍可能没有厂商，须在最终响应出口统一回退，避免回拉覆盖节点上报值。
	return modelPriceCatalogResponse{
		Model:                                         entry.Model,
		Provider:                                      entry.LiteLLMProvider,
		ModelVendor:                                   service.ModelPricingDisplayVendor(entry.ModelVendor, entry.LiteLLMProvider),
		InputModalities:                               entry.InputModalities,
		MaxInputTokens:                                entry.MaxInputTokens,
		MetadataSource:                                entry.MetadataSource,
		PriceFields:                                   entry.PriceFields,
		Mode:                                          entry.Mode,
		BillingMode:                                   entry.BillingMode,
		InputCostPer1MTokens:                          perTokenToPerMillion(entry.InputCostPerToken),
		InputCostPer1MTokensPriority:                  perTokenToPerMillion(entry.InputCostPerTokenPriority),
		OutputCostPer1MTokens:                         perTokenToPerMillion(entry.OutputCostPerToken),
		OutputCostPer1MTokensPriority:                 perTokenToPerMillion(entry.OutputCostPerTokenPriority),
		CacheCreationCostPer1MTokens:                  perTokenToPerMillion(entry.CacheCreationCost),
		CacheCreationCostPer1MTokensPriority:          perTokenToPerMillion(entry.CacheCreationCostPriority),
		CacheReadCostPer1MTokens:                      perTokenToPerMillion(entry.CacheReadCost),
		CacheReadCostPer1MTokensPriority:              perTokenToPerMillion(entry.CacheReadCostPriority),
		InputCostPer1MTokensAbove272K:                 perTokenToPerMillion(entry.InputCostPerTokenAbove272K),
		InputCostPer1MTokensPriorityAbove272K:         perTokenToPerMillion(entry.InputCostPerTokenPriorityAbove272K),
		OutputCostPer1MTokensAbove272K:                perTokenToPerMillion(entry.OutputCostPerTokenAbove272K),
		OutputCostPer1MTokensPriorityAbove272K:        perTokenToPerMillion(entry.OutputCostPerTokenPriorityAbove272K),
		CacheCreationCostPer1MTokensAbove272K:         perTokenToPerMillion(entry.CacheCreationCostAbove272K),
		CacheCreationCostPer1MTokensPriorityAbove272K: perTokenToPerMillion(entry.CacheCreationCostPriorityAbove272K),
		CacheReadCostPer1MTokensAbove272K:             perTokenToPerMillion(entry.CacheReadCostAbove272K),
		CacheReadCostPer1MTokensPriorityAbove272K:     perTokenToPerMillion(entry.CacheReadCostPriorityAbove272K),
		LongContextInputTokenThreshold:                entry.LongContextInputTokenThreshold,
		OutputCostPerImage:                            entry.OutputCostPerImage,
		OutputCostPerImageToken:                       entry.OutputCostPerImageToken,
		InputCostPerImageToken:                        entry.InputCostPerImageToken,
		PerRequestPrice:                               entry.PerRequestPrice,
		VideoPricePerSecond480P:                       entry.VideoPricePerSecond480P,
		VideoPricePerSecond720P:                       entry.VideoPricePerSecond720P,
		VideoPricePerSecond1080P:                      entry.VideoPricePerSecond1080P,
		Currency:                                      "USD",
		SupportsPromptCaching:                         entry.SupportsPromptCaching,
		SupportsServiceTier:                           entry.SupportsServiceTier,
		Source:                                        entry.Source,
		IsOverride:                                    entry.IsOverride,
		Disabled:                                      entry.Disabled,
	}
}

func perTokenToPerMillion(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return value * 1000000
}

func perMillionToPerToken(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return value / 1000000
}

// GetModelDefaultPricing 获取模型的默认定价（用于前端自动填充）
// GET /api/v1/admin/channels/model-pricing?model=claude-sonnet-4
func (h *ChannelHandler) GetModelDefaultPricing(c *gin.Context) {
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("MISSING_PARAMETER", "model parameter is required").
			WithMetadata(map[string]string{"param": "model"}))
		return
	}

	pricing, err := h.billingService.GetModelPricing(model)
	if err != nil {
		// 模型不在定价列表中
		response.Success(c, gin.H{"found": false})
		return
	}

	response.Success(c, gin.H{
		"found":              true,
		"input_price":        pricing.InputPricePerToken,
		"output_price":       pricing.OutputPricePerToken,
		"cache_write_price":  pricing.CacheCreationPricePerToken,
		"cache_read_price":   pricing.CacheReadPricePerToken,
		"image_input_price":  pricing.ImageInputPricePerToken,
		"image_output_price": pricing.ImageOutputPricePerToken,
	})
}

// platformToLiteLLMProvider maps a channel platform name to the corresponding
// LiteLLM provider string used as the key in the pricing catalog.
var platformToLiteLLMProvider = map[string]string{
	service.PlatformAnthropic:   "anthropic",
	service.PlatformOpenAI:      "openai",
	service.PlatformGemini:      "google",
	service.PlatformAntigravity: "anthropic",
	service.PlatformGrok:        "xai",
}

// SyncPricingModels 返回 LiteLLM 定价目录中指定平台的最新模型列表
// GET /api/v1/admin/channels/pricing/sync-models?platform=anthropic
func (h *ChannelHandler) SyncPricingModels(c *gin.Context) {
	platform := strings.ToLower(strings.TrimSpace(c.Query("platform")))
	if platform == "" {
		response.ErrorFrom(c, infraerrors.BadRequest("MISSING_PARAMETER", "platform parameter is required").
			WithMetadata(map[string]string{"param": "platform"}))
		return
	}

	provider, ok := platformToLiteLLMProvider[platform]
	if !ok {
		response.ErrorFrom(c, infraerrors.BadRequest("UNSUPPORTED_PLATFORM",
			fmt.Sprintf("unsupported platform: %s", platform)).
			WithMetadata(map[string]string{"param": "platform"}))
		return
	}

	models := h.pricingService.ListModelNamesByProvider(provider)
	response.Success(c, gin.H{"models": models})
}
