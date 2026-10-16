package middleware

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type GatewayLiteLocalAPIKeyResolver interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
	TouchLastUsed(ctx context.Context, keyID int64) error
}

// NewGatewayLiteHybridAPIKeyAuthMiddleware routes explicit glk_ keys to the
// gateway-local database and leaves every other key on the existing remote
// control-plane path. Prefix routing is deliberate: a remote outage must never
// silently reinterpret a main-site key as a local key.
func NewGatewayLiteHybridAPIKeyAuthMiddleware(localKeys GatewayLiteLocalAPIKeyResolver, remote APIKeyAuthMiddleware, cfg *config.Config) APIKeyAuthMiddleware {
	local := newGatewayLiteLocalAPIKeyAuthMiddleware(localKeys, cfg)
	return APIKeyAuthMiddleware(func(c *gin.Context) {
		rawKey := extractGatewayLiteAPIKey(c)
		if service.IsGatewayLiteLocalAPIKey(rawKey) {
			local(c)
			return
		}
		remote(c)
	})
}

func newGatewayLiteLocalAPIKeyAuthMiddleware(localKeys GatewayLiteLocalAPIKeyResolver, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if localKeys == nil {
			AbortWithError(c, 503, "LOCAL_API_KEY_AUTH_UNAVAILABLE", "Gateway-local API key authentication is unavailable")
			return
		}
		if strings.TrimSpace(c.Query("key")) != "" || strings.TrimSpace(c.Query("api_key")) != "" {
			AbortWithError(c, 400, "api_key_in_query_deprecated", "API key in query parameter is deprecated. Please use Authorization header instead.")
			return
		}
		rawKey := extractGatewayLiteAPIKey(c)
		if !service.IsGatewayLiteLocalAPIKey(rawKey) {
			AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
			return
		}

		apiKey, err := localKeys.GetByKey(c.Request.Context(), rawKey)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
				return
			}
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to validate gateway-local API key")
			return
		}
		SetOpsFallbackAPIKey(c, apiKey)
		if apiKey == nil || !apiKey.IsActive() {
			AbortWithError(c, 401, "API_KEY_DISABLED", "Gateway-local API key is disabled")
			return
		}
		if apiKey.User == nil || !apiKey.User.IsActive() || !apiKey.User.IsAdmin() {
			AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
			return
		}
		if apiKey.GroupID == nil || *apiKey.GroupID <= 0 || apiKey.Group == nil {
			AbortWithError(c, 503, service.GatewayGroupUnavailableCode, "Gateway-local API key group is unavailable")
			return
		}
		if !apiKey.Group.IsActive() {
			AbortWithError(c, 503, service.GatewayGroupUnavailableCode, "Gateway-local API key group is disabled")
			return
		}
		if !gatewayLiteLocalAPIKeyIPAllowed(c, apiKey, cfg) {
			AbortWithError(c, 403, "ACCESS_DENIED", fmt.Sprintf("Access denied. Your IP is %s", gatewayLiteLocalClientIP(c, cfg)))
			return
		}

		// Local keys are independent from main-site user quota and concurrency.
		// Clone the DB user so these runtime-only overrides cannot mutate cached data.
		localUser := *apiKey.User
		localUser.Concurrency = 0
		localUser.RPMLimit = 0
		localUser.TPMLimit = 0
		localUser.UserGroupRPMOverride = nil
		localUser.UserGroupRPMOverrideResolved = true
		apiKey.User = &localUser
		apiKey.GatewayStrategy = "fixed_group"
		apiKey.KeyDefaultGatewayNodeID = 1

		ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, localUser.ID)
		ctx = service.ContextWithGatewayLiteLocalAPIKey(ctx)
		c.Request = c.Request.WithContext(ctx)
		if err := freezeAPIKeyEffectiveRatePolicy(c, apiKey); err != nil {
			AbortWithError(c, 500, "BILLING_POLICY_INVALID", "Gateway-local API key billing policy is unavailable")
			return
		}
		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Set(string(ContextKeyUser), AuthSubject{UserID: localUser.ID, Concurrency: 0})
		c.Set(string(ContextKeyUserRole), localUser.Role)
		c.Set("auth_method", "gateway_lite_local_api_key")
		setGroupContext(c, apiKey.Group)
		// 本地密钥只携带统计所需的入口分组，不能创建 RequestMeta；后者是
		// 主站额度租约和异步任务结算身份，误用会让本地任务进入远程计费链。
		c.Request = c.Request.WithContext(gatewaylite.ContextWithIngressGroup(c.Request.Context(), apiKey.Group.ID))
		if !isBillingIntrospectionRequest(c) {
			_ = localKeys.TouchLastUsed(c.Request.Context(), apiKey.ID)
		}
		c.Next()
	}
}

func gatewayLiteLocalAPIKeyIPAllowed(c *gin.Context, apiKey *service.APIKey, cfg *config.Config) bool {
	if apiKey == nil || (len(apiKey.IPWhitelist) == 0 && len(apiKey.IPBlacklist) == 0) {
		return true
	}
	clientIP := gatewayLiteLocalClientIP(c, cfg)
	allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, apiKey.CompiledIPWhitelist, apiKey.CompiledIPBlacklist)
	return allowed
}

func gatewayLiteLocalClientIP(c *gin.Context, cfg *config.Config) string {
	return ip.GetSecurityClientIP(c, cfg != nil && cfg.TrustForwardedIPForAPIKeyACL())
}
