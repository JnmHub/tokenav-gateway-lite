package handler

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const keyBillingInfoSchemaVersion = 1

type keyBillingInfoResponse struct {
	Object                  string    `json:"object"`
	SchemaVersion           int       `json:"schema_version"`
	BillingScope            string    `json:"billing_scope"`
	GroupRateMultiplier     float64   `json:"group_rate_multiplier"`
	UserRateMultiplier      *float64  `json:"user_rate_multiplier,omitempty"`
	ResolvedRateMultiplier  float64   `json:"resolved_rate_multiplier"`
	PeakRateEnabled         bool      `json:"peak_rate_enabled"`
	PeakStart               string    `json:"peak_start,omitempty"`
	PeakEnd                 string    `json:"peak_end,omitempty"`
	PeakRateMultiplier      float64   `json:"peak_rate_multiplier,omitempty"`
	AppliedPeakMultiplier   float64   `json:"applied_peak_multiplier"`
	Timezone                string    `json:"timezone"`
	PolicyVersion           string    `json:"policy_version"`
	EffectiveRateMultiplier float64   `json:"effective_rate_multiplier"`
	ObservedAt              time.Time `json:"observed_at"`
}

// GatewayKeyBillingInfoHandler 返回当前 Key 的 Token 销售倍率。
// 它只消费鉴权 context（上下文），不接受用户/分组参数，
// 因此不存在通过 ID 越权查看其他 Key 的路径。
func GatewayKeyBillingInfoHandler(apiKeyService *service.APIKeyService, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg != nil && cfg.RunMode == config.RunModeSimple {
			writeKeyBillingError(c, http.StatusNotFound, "not_found_error", "Billing information is not supported in simple mode")
			return
		}
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil {
			writeKeyBillingError(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
			return
		}
		if apiKey.GroupID == nil || apiKey.Group == nil || apiKey.User == nil {
			writeKeyBillingError(c, http.StatusForbidden, "permission_error", "API key billing information is unavailable")
			return
		}

		userRate := apiKey.UserRateMultiplier
		// 主站 Key 的倍率已在快照中解析，禁止回源轻量网关的 shadow user（影子用户）。
		// 只有网关本地 Key 才读取本地用户专属分组倍率。
		if userRate == nil && service.IsGatewayLiteLocalAPIKeyContext(c.Request.Context()) && apiKeyService != nil {
			rates, err := apiKeyService.GetUserGroupRates(c.Request.Context(), apiKey.User.ID)
			if err != nil {
				writeKeyBillingError(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
				return
			}
			if rate, exists := rates[*apiKey.GroupID]; exists {
				userRate = &rate
			}
		}
		policy, ok := service.EffectiveRatePolicyFromContext(c.Request.Context())
		if !ok {
			var err error
			policy, err = service.ResolveEffectiveRate(apiKey.Group, userRate, time.Now())
			if err != nil {
				writeKeyBillingError(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
				return
			}
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, keyBillingInfoResponse{
			Object:                  "sub2api.key_billing",
			SchemaVersion:           keyBillingInfoSchemaVersion,
			BillingScope:            "token",
			GroupRateMultiplier:     policy.GroupRateMultiplier,
			UserRateMultiplier:      policy.UserRateMultiplier,
			ResolvedRateMultiplier:  policy.ResolvedRateMultiplier,
			PeakRateEnabled:         policy.PeakRateEnabled,
			PeakStart:               policy.PeakStart,
			PeakEnd:                 policy.PeakEnd,
			PeakRateMultiplier:      policy.PeakRateMultiplier,
			AppliedPeakMultiplier:   policy.AppliedPeakMultiplier,
			Timezone:                policy.Timezone,
			PolicyVersion:           policy.Version,
			EffectiveRateMultiplier: policy.EffectiveRateMultiplier,
			ObservedAt:              time.UnixMilli(policy.ResolvedAtMillis).UTC(),
		})
	}
}

func writeKeyBillingError(c *gin.Context, status int, errorType, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": errorType, "message": message}})
}
