package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

const imageTaskPolicySchemaVersion = 1

// ImageTaskPolicySnapshot is the minimum non-secret request identity needed to
// resume an accepted image task after a process restart. It deliberately does
// not contain the customer API key, account credentials, OAuth tokens, proxy
// URLs, password hashes, TOTP secrets or account bindings.
type ImageTaskPolicySnapshot struct {
	SchemaVersion             int                            `json:"schema_version"`
	APIKeyID                  int64                          `json:"api_key_id"`
	GatewayKeyID              string                         `json:"gateway_key_id,omitempty"`
	User                      ImageTaskUserSnapshot          `json:"user"`
	Group                     ImageTaskGroupSnapshot         `json:"group"`
	Subscription              *ImageTaskSubscriptionSnapshot `json:"subscription,omitempty"`
	UserRateMultiplier        *float64                       `json:"user_rate_multiplier,omitempty"`
	GatewayStrategy           string                         `json:"gateway_strategy,omitempty"`
	DefaultGatewayID          int64                          `json:"default_gateway_id,omitempty"`
	AllowedModels             []string                       `json:"allowed_models,omitempty"`
	TargetPlatform            string                         `json:"target_platform,omitempty"`
	RouteSource               string                         `json:"route_source,omitempty"`
	RequestedModel            string                         `json:"requested_model,omitempty"`
	QuotaPlatform             string                         `json:"quota_platform,omitempty"`
	UserAgent                 string                         `json:"user_agent,omitempty"`
	IPAddress                 string                         `json:"ip_address,omitempty"`
	SessionID                 string                         `json:"session_id,omitempty"`
	RequestHash               string                         `json:"request_hash,omitempty"`
	Channel                   ChannelUsageFields             `json:"channel,omitempty"`
	KeywordExcludedAccountIDs []int64                        `json:"keyword_excluded_account_ids,omitempty"`
	// BillingExempt is true only for a gateway-lite local key. The task keeps
	// this boundary flag, never the original customer credential.
	BillingExempt bool `json:"billing_exempt,omitempty"`
}

type ImageTaskUserSnapshot struct {
	ID          int64  `json:"id"`
	Tier        int    `json:"tier"`
	Role        string `json:"role,omitempty"`
	Status      string `json:"status"`
	Concurrency int    `json:"concurrency"`
	RPMLimit    int    `json:"rpm_limit,omitempty"`
	TPMLimit    int    `json:"tpm_limit,omitempty"`
}

type ImageTaskGroupSnapshot struct {
	ID                          int64                    `json:"id"`
	Name                        string                   `json:"name"`
	Platform                    string                   `json:"platform"`
	Status                      string                   `json:"status"`
	RateMultiplier              float64                  `json:"rate_multiplier"`
	PeakRateEnabled             bool                     `json:"peak_rate_enabled,omitempty"`
	PeakStart                   string                   `json:"peak_start,omitempty"`
	PeakEnd                     string                   `json:"peak_end,omitempty"`
	PeakRateMultiplier          float64                  `json:"peak_rate_multiplier,omitempty"`
	PeakTimezone                string                   `json:"peak_timezone,omitempty"`
	SubscriptionType            string                   `json:"subscription_type,omitempty"`
	DailyLimitUSD               *float64                 `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD              *float64                 `json:"weekly_limit_usd,omitempty"`
	MonthlyLimitUSD             *float64                 `json:"monthly_limit_usd,omitempty"`
	AllowImageGeneration        bool                     `json:"allow_image_generation"`
	ImageRateIndependent        bool                     `json:"image_rate_independent,omitempty"`
	ImageRateMultiplier         float64                  `json:"image_rate_multiplier,omitempty"`
	ImagePrice1K                *float64                 `json:"image_price_1k,omitempty"`
	ImagePrice2K                *float64                 `json:"image_price_2k,omitempty"`
	ImagePrice4K                *float64                 `json:"image_price_4k,omitempty"`
	AllowBlockedKeywordRequests bool                     `json:"allow_blocked_keyword_requests,omitempty"`
	MinimumTier                 *int                     `json:"minimum_tier,omitempty"`
	EffectiveRequiredTier       int                      `json:"effective_required_tier,omitempty"`
	ModelRouting                map[string][]int64       `json:"model_routing,omitempty"`
	ModelRoutingEnabled         bool                     `json:"model_routing_enabled,omitempty"`
	ModelMapping                map[string]string        `json:"model_mapping,omitempty"`
	RequireOAuthOnly            bool                     `json:"require_oauth_only,omitempty"`
	RequirePrivacySet           bool                     `json:"require_privacy_set,omitempty"`
	DefaultMappedModel          string                   `json:"default_mapped_model,omitempty"`
	RPMLimit                    int                      `json:"rpm_limit,omitempty"`
	MaxReasoningEffort          string                   `json:"max_reasoning_effort,omitempty"`
	ReasoningEffortMappings     []ReasoningEffortMapping `json:"reasoning_effort_mappings,omitempty"`
	UpdatedAt                   time.Time                `json:"updated_at"`
}

type ImageTaskSubscriptionSnapshot struct {
	ID                 int64      `json:"id"`
	UserID             int64      `json:"user_id"`
	GroupID            int64      `json:"group_id"`
	StartsAt           time.Time  `json:"starts_at"`
	ExpiresAt          time.Time  `json:"expires_at"`
	Status             string     `json:"status"`
	DailyWindowStart   *time.Time `json:"daily_window_start,omitempty"`
	WeeklyWindowStart  *time.Time `json:"weekly_window_start,omitempty"`
	MonthlyWindowStart *time.Time `json:"monthly_window_start,omitempty"`
	DailyUsageUSD      float64    `json:"daily_usage_usd,omitempty"`
	WeeklyUsageUSD     float64    `json:"weekly_usage_usd,omitempty"`
	MonthlyUsageUSD    float64    `json:"monthly_usage_usd,omitempty"`
}

func CaptureImageTaskPolicy(ctx context.Context, apiKey *APIKey, subscription *UserSubscription, channel ChannelUsageFields, userAgent, ipAddress, sessionID, requestHash string) (*ImageTaskPolicySnapshot, error) {
	if apiKey == nil || apiKey.User == nil || apiKey.Group == nil || apiKey.User.ID <= 0 || apiKey.Group.ID <= 0 {
		return nil, errors.New("image task policy context is incomplete")
	}
	group := apiKey.Group
	snapshot := &ImageTaskPolicySnapshot{
		SchemaVersion: imageTaskPolicySchemaVersion,
		APIKeyID:      apiKey.ID,
		User:          ImageTaskUserSnapshot{ID: apiKey.User.ID, Tier: apiKey.User.Tier, Role: apiKey.User.Role, Status: apiKey.User.Status, Concurrency: apiKey.User.Concurrency, RPMLimit: apiKey.User.RPMLimit, TPMLimit: apiKey.User.TPMLimit},
		Group: ImageTaskGroupSnapshot{
			ID: group.ID, Name: group.Name, Platform: group.Platform, Status: group.Status, RateMultiplier: group.RateMultiplier,
			PeakRateEnabled: group.PeakRateEnabled, PeakStart: group.PeakStart, PeakEnd: group.PeakEnd, PeakRateMultiplier: group.PeakRateMultiplier, PeakTimezone: group.PeakTimezone,
			SubscriptionType: group.SubscriptionType, DailyLimitUSD: cloneFloat64Ptr(group.DailyLimitUSD), WeeklyLimitUSD: cloneFloat64Ptr(group.WeeklyLimitUSD), MonthlyLimitUSD: cloneFloat64Ptr(group.MonthlyLimitUSD),
			AllowImageGeneration: group.AllowImageGeneration, ImageRateIndependent: group.ImageRateIndependent, ImageRateMultiplier: group.ImageRateMultiplier,
			ImagePrice1K: cloneFloat64Ptr(group.ImagePrice1K), ImagePrice2K: cloneFloat64Ptr(group.ImagePrice2K), ImagePrice4K: cloneFloat64Ptr(group.ImagePrice4K),
			AllowBlockedKeywordRequests: group.AllowBlockedKeywordRequests, MinimumTier: cloneIntPtr(group.MinimumTier), EffectiveRequiredTier: group.EffectiveRequiredTier,
			ModelRouting: cloneImageTaskModelRouting(group.ModelRouting), ModelRoutingEnabled: group.ModelRoutingEnabled,
			ModelMapping:     cloneImageTaskStringMap(group.ModelMapping),
			RequireOAuthOnly: group.RequireOAuthOnly, RequirePrivacySet: group.RequirePrivacySet, DefaultMappedModel: group.DefaultMappedModel,
			RPMLimit: group.RPMLimit, MaxReasoningEffort: group.MaxReasoningEffort, ReasoningEffortMappings: append([]ReasoningEffortMapping(nil), group.ReasoningEffortMappings...), UpdatedAt: group.UpdatedAt,
		},
		UserRateMultiplier: cloneFloat64Ptr(apiKey.UserRateMultiplier), GatewayStrategy: apiKey.GatewayStrategy,
		DefaultGatewayID: apiKey.KeyDefaultGatewayNodeID, AllowedModels: append([]string(nil), apiKey.AllowedModels...),
		QuotaPlatform: QuotaPlatform(ctx, apiKey), UserAgent: strings.TrimSpace(userAgent), IPAddress: strings.TrimSpace(ipAddress),
		SessionID: sanitizeSessionID(sessionID), RequestHash: strings.TrimSpace(requestHash), Channel: channel,
		BillingExempt: IsGatewayLiteLocalAPIKeyContext(ctx),
	}
	for accountID := range KeywordProtectedAccountIDsFromContext(ctx) {
		snapshot.KeywordExcludedAccountIDs = append(snapshot.KeywordExcludedAccountIDs, accountID)
	}
	sort.Slice(snapshot.KeywordExcludedAccountIDs, func(i, j int) bool {
		return snapshot.KeywordExcludedAccountIDs[i] < snapshot.KeywordExcludedAccountIDs[j]
	})
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		snapshot.GatewayKeyID = strings.TrimSpace(meta.KeyID)
	}
	if platform, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		snapshot.TargetPlatform = strings.TrimSpace(platform)
	}
	if source, ok := CompositeRouteSourceFromContext(ctx); ok {
		snapshot.RouteSource = strings.TrimSpace(source)
	}
	if model, ok := RequestedPublicModelFromContext(ctx); ok {
		snapshot.RequestedModel = strings.TrimSpace(model)
	}
	if subscription != nil {
		snapshot.Subscription = &ImageTaskSubscriptionSnapshot{
			ID: subscription.ID, UserID: subscription.UserID, GroupID: subscription.GroupID,
			StartsAt: subscription.StartsAt, ExpiresAt: subscription.ExpiresAt, Status: subscription.Status,
			DailyWindowStart: cloneTimePtr(subscription.DailyWindowStart), WeeklyWindowStart: cloneTimePtr(subscription.WeeklyWindowStart), MonthlyWindowStart: cloneTimePtr(subscription.MonthlyWindowStart),
			DailyUsageUSD: subscription.DailyUsageUSD, WeeklyUsageUSD: subscription.WeeklyUsageUSD, MonthlyUsageUSD: subscription.MonthlyUsageUSD,
		}
	}
	return snapshot, snapshot.Validate()
}

func (s *ImageTaskPolicySnapshot) Validate() error {
	if s == nil || s.SchemaVersion != imageTaskPolicySchemaVersion || s.User.ID <= 0 || s.Group.ID <= 0 {
		return errors.New("image task policy snapshot is invalid")
	}
	if strings.TrimSpace(s.Group.Platform) == "" || strings.TrimSpace(s.Group.Status) == "" || !s.Group.AllowImageGeneration {
		return errors.New("image task policy does not allow image generation")
	}
	return nil
}

func (s *ImageTaskPolicySnapshot) Marshal() (json.RawMessage, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(s)
	return json.RawMessage(payload), err
}

func DecodeImageTaskPolicy(payload json.RawMessage) (*ImageTaskPolicySnapshot, error) {
	var snapshot ImageTaskPolicySnapshot
	if len(payload) == 0 || json.Unmarshal(payload, &snapshot) != nil {
		return nil, errors.New("image task policy snapshot cannot be decoded")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *ImageTaskPolicySnapshot) APIKey() *APIKey {
	if s == nil {
		return nil
	}
	group := s.Group.Group()
	user := &User{ID: s.User.ID, Tier: s.User.Tier, Role: s.User.Role, Status: s.User.Status, Concurrency: s.User.Concurrency, RPMLimit: s.User.RPMLimit, TPMLimit: s.User.TPMLimit}
	groupID := group.ID
	return &APIKey{
		ID: s.APIKeyID, UserID: user.ID, Name: s.GatewayKeyID, GroupID: &groupID, Status: StatusAPIKeyActive,
		User: user, Group: group, UserRateMultiplier: cloneFloat64Ptr(s.UserRateMultiplier), AllowedModels: append([]string(nil), s.AllowedModels...),
		GatewayStrategy: s.GatewayStrategy, KeyDefaultGatewayNodeID: s.DefaultGatewayID,
	}
}

func (s *ImageTaskPolicySnapshot) SubscriptionValue() *UserSubscription {
	if s == nil || s.Subscription == nil {
		return nil
	}
	v := s.Subscription
	return &UserSubscription{
		ID: v.ID, UserID: v.UserID, GroupID: v.GroupID, StartsAt: v.StartsAt, ExpiresAt: v.ExpiresAt, Status: v.Status,
		DailyWindowStart: cloneTimePtr(v.DailyWindowStart), WeeklyWindowStart: cloneTimePtr(v.WeeklyWindowStart), MonthlyWindowStart: cloneTimePtr(v.MonthlyWindowStart),
		DailyUsageUSD: v.DailyUsageUSD, WeeklyUsageUSD: v.WeeklyUsageUSD, MonthlyUsageUSD: v.MonthlyUsageUSD,
	}
}

func (s ImageTaskGroupSnapshot) Group() *Group {
	return &Group{
		ID: s.ID, Name: s.Name, Platform: s.Platform, Status: s.Status, Hydrated: true, RateMultiplier: s.RateMultiplier,
		PeakRateEnabled: s.PeakRateEnabled, PeakStart: s.PeakStart, PeakEnd: s.PeakEnd, PeakRateMultiplier: s.PeakRateMultiplier, PeakTimezone: s.PeakTimezone,
		SubscriptionType: s.SubscriptionType, DailyLimitUSD: cloneFloat64Ptr(s.DailyLimitUSD), WeeklyLimitUSD: cloneFloat64Ptr(s.WeeklyLimitUSD), MonthlyLimitUSD: cloneFloat64Ptr(s.MonthlyLimitUSD),
		AllowImageGeneration: s.AllowImageGeneration, ImageRateIndependent: s.ImageRateIndependent, ImageRateMultiplier: s.ImageRateMultiplier,
		ImagePrice1K: cloneFloat64Ptr(s.ImagePrice1K), ImagePrice2K: cloneFloat64Ptr(s.ImagePrice2K), ImagePrice4K: cloneFloat64Ptr(s.ImagePrice4K),
		AllowBlockedKeywordRequests: s.AllowBlockedKeywordRequests, MinimumTier: cloneIntPtr(s.MinimumTier), EffectiveRequiredTier: s.EffectiveRequiredTier,
		ModelRouting: cloneImageTaskModelRouting(s.ModelRouting), ModelRoutingEnabled: s.ModelRoutingEnabled,
		ModelMapping:     cloneImageTaskStringMap(s.ModelMapping),
		RequireOAuthOnly: s.RequireOAuthOnly, RequirePrivacySet: s.RequirePrivacySet, DefaultMappedModel: s.DefaultMappedModel,
		RPMLimit: s.RPMLimit, MaxReasoningEffort: s.MaxReasoningEffort, ReasoningEffortMappings: append([]ReasoningEffortMapping(nil), s.ReasoningEffortMappings...), UpdatedAt: s.UpdatedAt,
	}
}

func cloneImageTaskModelRouting(input map[string][]int64) map[string][]int64 {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string][]int64, len(input))
	for key, values := range input {
		out[key] = append([]int64(nil), values...)
	}
	return out
}

func cloneImageTaskStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
