package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const gatewayLiteDefaultLeaseEstimateCents int64 = 1
const gatewayLiteFinalizeTimeout = 800 * time.Millisecond
const gatewayLiteLeaseRefillLockTTL = 5 * time.Second
const gatewayLiteLeasePrefetchTimeout = 2 * time.Second
const gatewayLiteLeaseRefreshTimeout = 3 * time.Second
const gatewayLiteLeaseRefreshAttempts = 2

var gatewayLiteResolveKeyGroup singleflight.Group
var gatewayLiteLeaseRefreshGroup singleflight.Group
var errGatewayLiteLeasePrefetchIncomplete = errors.New("low-watermark lease prefetch did not add capacity")

type GatewayLiteControlPlane interface {
	ResolveKey(ctx context.Context, req gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error)
	AcquireLease(ctx context.Context, req gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error)
	RebalanceLease(ctx context.Context, req gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error)
}

// GatewayLiteLocalGroupReader reads the gateway-local authoritative group
// metadata. The control plane still decides which gateway/group a key may use,
// while runtime capabilities and pricing must follow the local group immediately.
type GatewayLiteLocalGroupReader interface {
	GetGroup(ctx context.Context, groupID int64) (*service.Group, error)
}

// GatewayLiteSettlementPolicyReader is intentionally a one-value in-memory
// reader: request authentication must never query PostgreSQL on the hot path.
type GatewayLiteSettlementPolicyReader interface {
	GatewayLiteIdempotentSettlementEnabled() bool
}

// NewGatewayLiteAPIKeyAuthMiddleware validates API keys against the control
// plane and uses regional Redis as the hot-path key/lease cache.
func NewGatewayLiteAPIKeyAuthMiddleware(control GatewayLiteControlPlane, region, gatewayCode string, quota *gatewaylite.RedisQuota, keyCache *gatewaylite.RedisKeyCache, usageReporter gatewaylite.UsageReportClient) APIKeyAuthMiddleware {
	redisPrefix := ""
	if quota != nil {
		redisPrefix = quota.Prefix()
	} else if keyCache != nil {
		redisPrefix = keyCache.Prefix()
	} else if reporter, ok := usageReporter.(*gatewaylite.RedisUsageReporter); ok && reporter != nil {
		redisPrefix = reporter.Prefix()
	}
	return NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfig(control, gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{
		Region:      region,
		GatewayCode: gatewayCode,
		RedisPrefix: redisPrefix,
	}), quota, keyCache, usageReporter)
}

func NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfig(control GatewayLiteControlPlane, runtimeRef *gatewaylite.RuntimeConfigRef, quota *gatewaylite.RedisQuota, keyCache *gatewaylite.RedisKeyCache, usageReporter gatewaylite.UsageReportClient) APIKeyAuthMiddleware {
	return NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(control, runtimeRef, quota, keyCache, usageReporter, nil)
}

func NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(control GatewayLiteControlPlane, runtimeRef *gatewaylite.RuntimeConfigRef, quota *gatewaylite.RedisQuota, keyCache *gatewaylite.RedisKeyCache, usageReporter gatewaylite.UsageReportClient, localGroups GatewayLiteLocalGroupReader, settlementPolicies ...GatewayLiteSettlementPolicyReader) APIKeyAuthMiddleware {
	return NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter(
		control, runtimeRef, quota, keyCache, usageReporter, localGroups, nil, settlementPolicies...,
	)
}

// NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter
// wires invalid-key abuse protection only into the main-site Key path. Gateway-
// local glk_ keys stay independent from the control-plane snapshot protocol.
func NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter(control GatewayLiteControlPlane, runtimeRef *gatewaylite.RuntimeConfigRef, quota *gatewaylite.RedisQuota, keyCache *gatewaylite.RedisKeyCache, usageReporter gatewaylite.UsageReportClient, localGroups GatewayLiteLocalGroupReader, invalidAuthLimiter *gatewaylite.InvalidAuthAbuseLimiter, settlementPolicies ...GatewayLiteSettlementPolicyReader) APIKeyAuthMiddleware {
	return APIKeyAuthMiddleware(func(c *gin.Context) {
		startedAt := time.Now()
		settlementMode := gatewaylite.BillingSettlementModeLegacy
		if len(settlementPolicies) > 0 && settlementPolicies[0] != nil && settlementPolicies[0].GatewayLiteIdempotentSettlementEnabled() {
			settlementMode = gatewaylite.BillingSettlementModeIdempotentV2
		}
		perTurnSettlement := settlementMode == gatewaylite.BillingSettlementModeIdempotentV2 && gatewayLiteRequestUsesPerTurnSettlement(c)
		runtimeCfg := gatewaylite.NormalizeRuntimeConfig(gatewaylite.RuntimeConfig{})
		if runtimeRef != nil {
			runtimeCfg = runtimeRef.Current()
		}
		scope := runtimeCfg.GatewayScope()
		region := scope
		gatewayCode := runtimeCfg.GatewayCode
		requestQuota := quota
		if quota != nil {
			requestQuota = quota.WithPrefix(runtimeCfg.RedisPrefix)
		}
		requestKeyCache := keyCache
		if keyCache != nil {
			requestKeyCache = keyCache.WithPrefix(runtimeCfg.RedisPrefix)
		}
		requestUsageReporter := usageReporter
		if reporter, ok := usageReporter.(*gatewaylite.RedisUsageReporter); ok && reporter != nil {
			requestUsageReporter = reporter.WithPrefix(runtimeCfg.RedisPrefix)
		}
		rawKey := extractGatewayLiteAPIKey(c)
		invalidAuthSource := SecurityClientIP(c)
		checkInvalidAuthBlock := func() bool {
			if retryAfter, blocked := invalidAuthLimiter.Check(c.Request.Context(), invalidAuthSource, rawKey); blocked {
				abortGatewayLiteInvalidAuthAbuse(c, retryAfter)
				return true
			}
			return false
		}
		if rawKey == "" {
			if checkInvalidAuthBlock() {
				return
			}
			invalidAuthLimiter.RecordFailure(c.Request.Context(), invalidAuthSource, rawKey)
			AbortWithError(c, 401, "API_KEY_REQUIRED", "API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header")
			return
		}

		delegated := gatewaylite.IsOAuthInvocationCredential(rawKey)
		var invocation *gatewaylite.OAuthInvocationResolveResponse
		keyID, secret, ok := splitGatewayLiteAPIKey(rawKey)
		if delegated {
			invocationControl, supported := control.(gatewaylite.OAuthInvocationControlPlane)
			if !supported {
				AbortWithError(c, 503, "OAUTH_INVOCATION_UNAVAILABLE", "受限委托调用暂不可用")
				return
			}
			var err error
			invocation, err = gatewaylite.ResolveOAuthInvocationCredential(c.Request.Context(), invocationControl, rawKey, gatewayCode, region)
			if err != nil {
				AbortWithError(c, 403, "OAUTH_INVOCATION_DENIED", "受限委托已过期或不允许此调用")
				return
			}
			invocationCtx, err := gatewaylite.ContextWithOAuthInvocationCredential(c.Request.Context(), rawKey, gatewayCode, region, invocation)
			if err != nil {
				AbortWithError(c, 403, "OAUTH_INVOCATION_DENIED", "受限委托信息无效")
				return
			}
			c.Request = c.Request.WithContext(invocationCtx)
			keyID = invocation.Key.KeyID
			ok = true
		}
		if !ok {
			if checkInvalidAuthBlock() {
				return
			}
			invalidAuthLimiter.RecordFailure(c.Request.Context(), invalidAuthSource, rawKey)
			AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
			return
		}
		// 已命中进程内正缓存且 secret（密钥秘密段）匹配时，这个请求已经有
		// 足够强的合法性证据，不应再被同 NAT 的非法 Key 统计拖去等待 Redis。
		// 正式授权仍在下方 ResolveKey 完成，快照失效语义保持不变。
		hotPositive := false
		if cached, cachedOK := requestKeyCache.PeekHotImmutable(keyID, region); cachedOK && cached.Active() {
			hotPositive = gatewayLiteSecretMatches(secret, cached.SecretHash)
		}
		if !hotPositive && checkInvalidAuthBlock() {
			return
		}

		billableRequest := gatewayLiteRequestBillable(c)
		var authCacheStartedAt time.Time
		if servertiming.Active(c.Request.Context()) {
			authCacheStartedAt = time.Now()
		}
		var key *gatewaylite.KeySnapshot
		var err error
		if delegated {
			key = invocation.Key
		} else {
			key, err = gatewayLiteResolveKey(c.Request.Context(), control, requestKeyCache, keyID, region, gatewayCode, !billableRequest, isBillingIntrospectionRequest(c))
		}
		if !authCacheStartedAt.IsZero() {
			servertiming.Add(c.Request.Context(), servertiming.StageAuth, time.Since(authCacheStartedAt))
		}
		if err != nil {
			var rejected gatewayLiteResolveRejectedError
			if errors.As(err, &rejected) {
				if gatewayLiteResolveRejectionIsInvalidAuth(rejected.Code) {
					invalidAuthLimiter.RecordFailure(c.Request.Context(), invalidAuthSource, rawKey)
				}
				abortGatewayLiteResolveRejected(c, rejected.Code)
				return
			}
			AbortWithError(c, 503, "CONTROL_PLANE_UNAVAILABLE", "Failed to validate API key")
			return
		}
		if key == nil || !key.Active() {
			invalidAuthLimiter.RecordFailure(c.Request.Context(), invalidAuthSource, rawKey)
			AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
			return
		}
		if !delegated && !gatewayLiteSecretMatches(secret, key.SecretHash) {
			invalidAuthLimiter.RecordFailure(c.Request.Context(), invalidAuthSource, rawKey)
			AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
			return
		}
		if !gatewayLiteFixedGroupBindingValid(*key) {
			AbortWithError(c, 403, "FIXED_GROUP_REQUIRED", "This API key must be bound to a gateway group")
			return
		}
		if !gatewayLiteGatewayAllowed(*key, region, gatewayCode) {
			AbortWithError(c, 403, "GATEWAY_NOT_ALLOWED", "Gateway is not available for this API key")
			return
		}
		apiKey := gatewayLiteSnapshotToAPIKey(rawKey, *key)
		if !gatewayLiteRemoteAPIKeyIPAllowed(c, apiKey) {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonIPRestriction)
			AbortWithError(c, http.StatusForbidden, "ACCESS_DENIED", "Access denied by API key IP policy")
			return
		}
		if localGroups != nil {
			// 节点等级只存在于主站快照，本地分组则是分组配置的运行时权威。
			// 替换成本地对象前先保留主站最终门槛，之后取两边较高值，避免
			// “节点 T1 + 分组继承”在本地加载后意外退化成 T0。
			snapshotRequiredTier := service.GroupRequiredTier(apiKey.Group)
			localGroupID := gatewayLiteGroupID(apiKey.Group)
			localGroup, localGroupErr := localGroups.GetGroup(c.Request.Context(), localGroupID)
			if localGroupErr != nil {
				AbortWithError(c, 503, service.GatewayGroupUnavailableCode, "Failed to load gateway group configuration")
				return
			}
			if localGroup == nil || !localGroup.IsActive() {
				AbortWithError(c, 503, service.GatewayGroupUnavailableCode, "Gateway group is unavailable on this gateway")
				return
			}
			if localGroup.LocalOnly {
				AbortWithError(c, http.StatusForbidden, "GROUP_LOCAL_ONLY", "This gateway group only accepts API keys created locally on the gateway")
				return
			}
			// The stable binding ID comes from the control plane, but every mutable
			// runtime rule comes from gateway-lite so local edits affect the next request.
			if localRequiredTier := service.GroupRequiredTier(localGroup); localRequiredTier > snapshotRequiredTier {
				snapshotRequiredTier = localRequiredTier
			}
			localGroupView := *localGroup
			localGroupView.EffectiveRequiredTier = snapshotRequiredTier
			apiKey.Group = &localGroupView
			apiKey.GroupID = &localGroupView.ID
		}
		if !service.GroupTierAllowedForUser(apiKey.User, apiKey.Group) {
			AbortWithError(c, 403, "GROUP_TIER_REQUIRED", "Your current tier cannot use this gateway group")
			return
		}
		requestModel, err := gatewayLiteRequestModel(c)
		if err != nil {
			ingress.Record(ingress.RejectMalformed)
			AbortWithError(c, 400, "INVALID_REQUEST_BODY", "Invalid request body")
			return
		}
		if code, message := GatewayModelPermissionError(apiKey, requestModel); code != "" {
			AbortWithError(c, http.StatusForbidden, code, message)
			return
		}
		authLatencyMillis := time.Since(startedAt).Milliseconds()

		requestID := gatewayLiteRequestID(c)
		SetOpsFallbackAPIKey(c, apiKey)
		c.Set(string(ContextKeyAPIKey), apiKey)
		requestContext := context.WithValue(c.Request.Context(), ctxkey.UserID, apiKey.User.ID)
		c.Request = c.Request.WithContext(requestContext)
		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), apiKey.User.Role)
		setGroupContext(c, apiKey.Group)
		requestMeta := gatewayLiteRequestMeta(
			requestID, key.KeyID, key.UserID, "", region, gatewayCode,
			apiKey.Group, apiKey.UserRateMultiplier, settlementMode, startedAt,
		)
		requestMeta.UserEmail = strings.TrimSpace(apiKey.User.Email)
		requestMeta.Username = strings.TrimSpace(apiKey.User.Username)
		requestMeta.OrganizationID = key.OrganizationID
		requestMeta.OrganizationName = strings.TrimSpace(key.OrganizationName)
		requestMeta.OrganizationRole = strings.TrimSpace(key.OrganizationRole)
		requestMeta.BillingSubjectType = strings.TrimSpace(key.BillingSubjectType)
		requestMeta.BillingSubjectID = key.BillingSubjectID
		requestMeta.OrganizationConcurrency = key.OrganizationConcurrency
		requestMeta.OrganizationRateLimitRPM = key.OrganizationRateLimitRPM
		requestMeta.OrganizationRateLimitTPM = key.OrganizationRateLimitTPM
		requestMeta.MemberConcurrency = key.MemberConcurrency
		requestMeta.MemberRateLimitRPM = key.MemberRateLimitRPM
		requestMeta.MemberRateLimitTPM = key.MemberRateLimitTPM
		requestMeta.EntryGatewayNodeID = key.KeyDefaultGatewayNodeID
		requestMeta.EntryGlobalGroupID = key.Group.ID
		if delegated {
			if !gatewayLiteRequestUsesPerTurnSettlement(c) && !gatewaylite.OAuthInvocationEndpointSupported(c.Request.URL.Path) {
				AbortWithError(c, 400, "OAUTH_INVOCATION_UNSUPPORTED", "受限委托首期仅支持同步文本请求")
				return
			}
			c.Request = c.Request.WithContext(gatewaylite.ContextWithRequestMeta(c.Request.Context(), requestMeta))
			c.Next()
			finalMeta, _ := gatewaylite.RequestMetaFromContext(c.Request.Context())
			if !gatewayLiteRequestUsesPerTurnSettlement(c) {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
				if !finalMeta.UpstreamConsumed && (finalMeta.Funding == nil || !finalMeta.Funding.Dispatched()) {
					if err := gatewaylite.ReleaseFundingPreDispatch(recoveryCtx, "delegated_http_pre_dispatch_failure"); err != nil {
						log.Printf("gateway-lite delegated pre-dispatch release pending request=%s", requestMeta.RequestID)
					}
				}
				if err := gatewaylite.FinishOAuthInvocationRecovery(recoveryCtx); err != nil {
					log.Printf("gateway-lite delegated durable recovery pending request=%s", requestMeta.RequestID)
				}
				cancel()
			}
			return
		}
		if gatewayLiteRequestUsesPerTurnSettlement(c) {
			admit := gatewayLiteTurnAdmission(control, requestQuota, *key, requestMeta, c.Request.URL.Path)
			if key.TokenUse == "agent_api_key" {
				// 敏感摘要与入口身份只由请求内闭包持有；不写共享快照、日志或响应。
				admit = gatewayLiteAgentTurnAdmission(control, localGroups, *key, requestMeta, apiKey.Group, SecurityClientIP(c), admit)
			}
			turnCtx := gatewaylite.ContextWithRequestMeta(c.Request.Context(), requestMeta)
			turnCtx = gatewaylite.ContextWithTurnAdmission(turnCtx, admit)
			c.Request = c.Request.WithContext(turnCtx)
			c.Next()
			return
		}
		if !billableRequest {
			c.Request = c.Request.WithContext(gatewaylite.ContextWithRequestMeta(c.Request.Context(), requestMeta))
			c.Next()
			return
		}
		if gatewayLiteFundingAdmission(c, control, requestQuota, requestUsageReporter, *key, requestMeta, requestModel) {
			return
		}
		initialEstimatedCents := gatewaylite.SalesRateReservationCents(requestMeta.RateMultiplier)

		quotaStart := time.Now()
		lease, err := gatewayLiteReserveQuota(c.Request.Context(), control, requestQuota, *key, region, requestID, initialEstimatedCents)
		quotaLatencyMillis := time.Since(quotaStart).Milliseconds()
		servertiming.Add(c.Request.Context(), servertiming.StageQuota, time.Since(quotaStart))
		if err != nil {
			var quotaErr gatewayLiteQuotaRejectedError
			if errors.As(err, &quotaErr) {
				if quotaErr.Code == "usage_limit_exceeded" || quotaErr.Code == "insufficient_balance" {
					AbortWithError(c, 429, "INSUFFICIENT_QUOTA", gatewayLiteQuotaErrorMessage(quotaErr.Detail))
					gatewayLiteReportPreDispatchFailure(requestUsageReporter, gatewayLitePreDispatchFailureInput{
						RequestID: requestID, KeyID: key.KeyID, UserID: key.UserID, OrganizationID: key.OrganizationID,
						BillingSubjectType: key.BillingSubjectType, BillingSubjectID: key.BillingSubjectID, Region: region, GatewayID: gatewayCode,
						Group: apiKey.Group, RateMultiplier: requestMeta.RateMultiplier, RatePolicyAudit: requestMeta.RatePolicyAudit,
						EstimatedCents: initialEstimatedCents, Method: c.Request.Method, Path: c.Request.URL.Path, Model: requestModel,
						Status: c.Writer.Status(), ErrorMessage: quotaErr.Error(), StartedAt: startedAt,
						AuthLatency: authLatencyMillis, QuotaLatency: quotaLatencyMillis,
					})
					return
				}
				if gatewayLiteQuotaCodeIsAccessRejection(quotaErr.Code) {
					abortGatewayLiteResolveRejected(c, quotaErr.Code)
					gatewayLiteReportPreDispatchFailure(requestUsageReporter, gatewayLitePreDispatchFailureInput{
						RequestID: requestID, KeyID: key.KeyID, UserID: key.UserID, OrganizationID: key.OrganizationID,
						BillingSubjectType: key.BillingSubjectType, BillingSubjectID: key.BillingSubjectID, Region: region, GatewayID: gatewayCode,
						Group: apiKey.Group, RateMultiplier: requestMeta.RateMultiplier, RatePolicyAudit: requestMeta.RatePolicyAudit,
						EstimatedCents: initialEstimatedCents, Method: c.Request.Method, Path: c.Request.URL.Path, Model: requestModel,
						Status: c.Writer.Status(), ErrorMessage: quotaErr.Error(), StartedAt: startedAt,
						AuthLatency: authLatencyMillis, QuotaLatency: quotaLatencyMillis,
					})
					return
				}
				AbortWithError(c, 503, "QUOTA_UNAVAILABLE", "Failed to reserve quota")
				gatewayLiteReportPreDispatchFailure(requestUsageReporter, gatewayLitePreDispatchFailureInput{
					RequestID: requestID, KeyID: key.KeyID, UserID: key.UserID, OrganizationID: key.OrganizationID,
					BillingSubjectType: key.BillingSubjectType, BillingSubjectID: key.BillingSubjectID, Region: region, GatewayID: gatewayCode,
					Group: apiKey.Group, RateMultiplier: requestMeta.RateMultiplier, RatePolicyAudit: requestMeta.RatePolicyAudit,
					EstimatedCents: initialEstimatedCents, Method: c.Request.Method, Path: c.Request.URL.Path, Model: requestModel,
					Status: c.Writer.Status(), ErrorMessage: quotaErr.Error(), StartedAt: startedAt,
					AuthLatency: authLatencyMillis, QuotaLatency: quotaLatencyMillis,
				})
				return
			}
			AbortWithError(c, 503, "QUOTA_UNAVAILABLE", "Failed to reserve quota")
			gatewayLiteReportPreDispatchFailure(requestUsageReporter, gatewayLitePreDispatchFailureInput{
				RequestID: requestID, KeyID: key.KeyID, UserID: key.UserID, OrganizationID: key.OrganizationID,
				BillingSubjectType: key.BillingSubjectType, BillingSubjectID: key.BillingSubjectID, Region: region, GatewayID: gatewayCode,
				Group: apiKey.Group, RateMultiplier: requestMeta.RateMultiplier, RatePolicyAudit: requestMeta.RatePolicyAudit,
				EstimatedCents: initialEstimatedCents, Method: c.Request.Method, Path: c.Request.URL.Path, Model: requestModel,
				Status: c.Writer.Status(), ErrorMessage: err.Error(), StartedAt: startedAt,
				AuthLatency: authLatencyMillis, QuotaLatency: quotaLatencyMillis,
			})
			return
		}
		if lease == nil {
			AbortWithError(c, 429, "INSUFFICIENT_QUOTA", "Insufficient quota")
			gatewayLiteReportPreDispatchFailure(requestUsageReporter, gatewayLitePreDispatchFailureInput{
				RequestID: requestID, KeyID: key.KeyID, UserID: key.UserID, OrganizationID: key.OrganizationID,
				BillingSubjectType: key.BillingSubjectType, BillingSubjectID: key.BillingSubjectID, Region: region, GatewayID: gatewayCode,
				Group: apiKey.Group, RateMultiplier: requestMeta.RateMultiplier, RatePolicyAudit: requestMeta.RatePolicyAudit,
				EstimatedCents: initialEstimatedCents, Method: c.Request.Method, Path: c.Request.URL.Path, Model: requestModel,
				Status: c.Writer.Status(), ErrorMessage: "quota reservation returned no lease", StartedAt: startedAt,
				AuthLatency: authLatencyMillis, QuotaLatency: quotaLatencyMillis,
			})
			return
		}
		if stats := gatewaylite.DefaultRuntimeStats(); stats != nil {
			stats.RecordUser(key.UserID, startedAt)
		}

		c.Set(string(ContextKeyGatewayLiteLease), *lease)
		reservation := gatewaylite.NewRequestReservation(initialEstimatedCents, lease.LeaseID)
		requestMeta.LeaseID = lease.LeaseID
		requestMeta.Reservation = reservation
		c.Request = c.Request.WithContext(gatewaylite.ContextWithRequestMeta(c.Request.Context(), requestMeta))
		perfDiagnostics := strings.TrimSpace(c.GetHeader("X-Gateway-Perf-Diagnostics")) == "1"
		if perfDiagnostics {
			// 仅隔离压测显式请求时声明 Trailer；普通客户端不会多出响应字段。
			c.Writer.Header().Add("Trailer", "X-Gateway-Finalize-Ms")
		}
		// gin.Context（请求上下文）不能交给异步上报；在进入下游前只拍下已校验的标量。
		sessionID := service.ExtractClientSessionID(c)
		c.Next()
		requestMeta, _ = gatewaylite.RequestMetaFromContext(c.Request.Context())
		estimatedCents, finalizedLeaseID, forceRefund := reservation.Snapshot()
		if estimatedCents <= 0 {
			estimatedCents = gatewayLiteDefaultLeaseEstimateCents
		}
		if finalizedLeaseID == "" {
			finalizedLeaseID = lease.LeaseID
		}

		routingLatency := gatewayLiteContextLatencyMillis(c, service.OpsRoutingLatencyMsKey)
		finalizeStartedAt := time.Now()
		gatewayLiteFinalizeUsage(requestUsageReporter, requestQuota, gatewayLiteFinalizeInput{
			RequestID:                 requestID,
			KeyID:                     key.KeyID,
			SessionID:                 sessionID,
			UserID:                    key.UserID,
			OrganizationID:            key.OrganizationID,
			BillingSubjectType:        key.BillingSubjectType,
			BillingSubjectID:          key.BillingSubjectID,
			LeaseID:                   finalizedLeaseID,
			Region:                    region,
			GatewayID:                 gatewayLiteCurrentGatewayID(region, gatewayCode),
			GroupID:                   requestMeta.GroupID,
			GroupName:                 requestMeta.GroupName,
			RateMultiplier:            requestMeta.RateMultiplier,
			RatePolicyAudit:           requestMeta.RatePolicyAudit,
			RequestedGroupID:          requestMeta.OriginalGroupID,
			RequestedGroupName:        requestMeta.OriginalGroupName,
			KeywordPolicyMatched:      requestMeta.KeywordPolicyMatched,
			KeywordPolicyRerouted:     requestMeta.KeywordPolicyRerouted,
			KeywordPolicySource:       requestMeta.KeywordPolicySource,
			KeywordPolicyReason:       requestMeta.KeywordPolicyReason,
			DetectedLanguages:         append([]string(nil), requestMeta.DetectedLanguages...),
			RequestedReasoningEffort:  requestMeta.RequestedReasoningEffort,
			ReasoningEffort:           requestMeta.ReasoningEffort,
			Method:                    c.Request.Method,
			Path:                      c.Request.URL.Path,
			Model:                     requestModel,
			Status:                    c.Writer.Status(),
			EstimatedCents:            estimatedCents,
			StartedAt:                 startedAt,
			EndedAt:                   time.Now(),
			AuthLatency:               authLatencyMillis,
			QuotaLatency:              quotaLatencyMillis,
			RoutingLatency:            routingLatency,
			RoutingBreakdown:          gatewayLiteRoutingLatencyBreakdown(c, routingLatency),
			UpstreamLatency:           gatewayLiteContextLatencyMillis(c, service.OpsUpstreamLatencyMsKey),
			ResponseLatency:           gatewayLiteContextLatencyMillis(c, service.OpsResponseLatencyMsKey),
			Aborted:                   c.IsAborted(),
			ForceRefund:               forceRefund,
			BillingSettlementMode:     requestMeta.BillingSettlementMode,
			PerTurnSettlement:         perTurnSettlement,
			AsyncSettlement:           requestMeta.AsyncSettlement,
			AsyncPersistenceUncertain: requestMeta.AsyncPersistenceUncertain,
			UpstreamConsumed:          requestMeta.UpstreamConsumed,
		})
		servertiming.Add(c.Request.Context(), servertiming.StageSettle, time.Since(finalizeStartedAt))
		if perfDiagnostics {
			c.Writer.Header().Set("X-Gateway-Finalize-Ms", strconv.FormatInt(time.Since(finalizeStartedAt).Milliseconds(), 10))
		}
	})
}

func gatewayLiteResolveRejectionIsInvalidAuth(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "", "not_found", "invalid_api_key":
		return true
	default:
		return false
	}
}

func abortGatewayLiteInvalidAuthAbuse(c *gin.Context, retryAfter time.Duration) {
	ingress.Record(ingress.RejectAuthRateLimit)
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	AbortWithError(c, http.StatusTooManyRequests, "INVALID_AUTH_RATE_LIMITED", "Too many invalid API key attempts")
}

func gatewayLiteRequestBillable(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return true
	}
	method := strings.ToUpper(strings.TrimSpace(c.Request.Method))
	path := strings.TrimRight(c.Request.URL.Path, "/")
	// Ark 查询/删除只访问已归属的任务；完成时修正原创建请求，不能按轮询次数收费。
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodDelete {
		for _, prefix := range []string{"/v1", "/api/v3", "/v3", ""} {
			if strings.HasPrefix(path, prefix+"/contents/generations/tasks/") {
				return false
			}
		}
	}
	switch method {
	case http.MethodPost:
		// Live 当前没有可核验 Token 用量，只写零费用会话审计；创建请求不能
		// 走普通一美分预占，否则会在“免费但不可计费”的协议上错误扣款。
		if path == "/v1/live" || path == "/backend-api/codex/realtime/calls" {
			return false
		}
		if (strings.HasPrefix(path, "/v1/images/tasks/") || strings.HasPrefix(path, "/images/tasks/")) && strings.HasSuffix(path, "/cancel") {
			return false
		}
	case http.MethodGet, http.MethodHead:
		switch path {
		case "/backend-api/codex/models", "/models", "/v1/models", "/v1beta/models", "/antigravity/models", "/antigravity/v1/models", "/antigravity/v1beta/models", "/v1/sub2api/billing":
			return false
		}
		// 视频生成已经在原始 POST 请求上完成额度预占，并在异步任务结束后
		// 通过原 request_id 做最终差额结算。状态查询只是读取任务进度，若再次
		// 预扣一美分，会让客户端的正常轮询次数直接变成额外费用。
		if strings.HasPrefix(path, "/v1/videos/") || strings.HasPrefix(path, "/videos/") {
			return false
		}
		if path == "/v1/images/tasks" || path == "/images/tasks" || strings.HasPrefix(path, "/v1/images/tasks/") || strings.HasPrefix(path, "/images/tasks/") {
			return false
		}
		if strings.HasPrefix(path, "/v1/live/") || strings.HasPrefix(path, "/backend-api/codex/") {
			return false
		}
	}
	return true
}

// gatewayLiteRequestUsesPerTurnSettlement identifies Responses WebSocket
// ingress. The HTTP upgrade reserves one quota slot for the whole connection,
// while every completed resp_* turn is an independent billable usage event.
func gatewayLiteRequestUsesPerTurnSettlement(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(c.GetHeader("Upgrade")), "websocket") {
		return false
	}
	connectionHasUpgrade := false
	for _, token := range strings.Split(c.GetHeader("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			connectionHasUpgrade = true
			break
		}
	}
	if !connectionHasUpgrade {
		return false
	}
	path := strings.TrimRight(c.Request.URL.Path, "/")
	return path == "/responses" || path == "/v1/responses" || path == "/backend-api/codex/responses"
}

func gatewayLiteGatewayAllowed(snapshot gatewaylite.KeySnapshot, region, gatewayCode string) bool {
	if !gatewayLiteFixedGroupBindingValid(snapshot) || !snapshot.GatewayAccessEnforced {
		return false
	}
	current := gatewayLiteCurrentGatewayID(region, gatewayCode)
	for _, gateway := range snapshot.AvailableGateways {
		if current != "" && strings.EqualFold(strings.TrimSpace(gateway.Code), current) {
			return gateway.ID == snapshot.KeyDefaultGatewayNodeID
		}
		if strings.TrimSpace(gateway.Code) == "" && strings.EqualFold(strings.TrimSpace(gateway.Region), strings.TrimSpace(region)) {
			return gateway.ID == snapshot.KeyDefaultGatewayNodeID
		}
	}
	return false
}

func gatewayLiteFixedGroupBindingValid(snapshot gatewaylite.KeySnapshot) bool {
	return strings.EqualFold(strings.TrimSpace(snapshot.GatewayStrategy), "fixed_group") &&
		snapshot.KeyDefaultGatewayNodeID > 0 &&
		snapshot.GroupID > 0 &&
		snapshot.Group != nil &&
		snapshot.Group.ID > 0
}

func gatewayLiteRequestModel(c *gin.Context) (string, error) {
	if c == nil || c.Request == nil {
		return "", nil
	}
	// Gemini-style routes encode the model in the path rather than the JSON
	// body.  Keep /v1/models itself excluded because it is a discovery request.
	if model := gatewayLiteModelFromPath(c.Request.URL.Path); model != "" {
		return model, nil
	}
	// A few compatible endpoints put the model in the query string (for
	// example Realtime/voice clients).  Checking it here keeps those routes
	// under the same group allow-list gate as JSON requests.
	if model := strings.TrimSpace(c.Query("model")); model != "" {
		return model, nil
	}
	if c.Request.Body == nil {
		return "", nil
	}
	method := strings.ToUpper(strings.TrimSpace(c.Request.Method))
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return "", nil
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	contentType := strings.TrimSpace(c.GetHeader("Content-Type"))
	mediaType, params, parseErr := mime.ParseMediaType(contentType)
	if parseErr == nil {
		switch strings.ToLower(mediaType) {
		case "multipart/form-data":
			if model := gatewayLiteMultipartModel(body, params["boundary"]); model != "" {
				return model, nil
			}
			return "", nil
		case "application/x-www-form-urlencoded":
			if values, err := url.ParseQuery(string(body)); err == nil {
				if model := strings.TrimSpace(values.Get("model")); model != "" {
					return model, nil
				}
			}
			return "", nil
		}
	}
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "json") {
		return "", nil
	}
	var payload struct {
		Model   string          `json:"model"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil
	}
	if model := strings.TrimSpace(payload.Model); model != "" {
		return model, nil
	}
	if len(payload.Session) > 0 {
		var session struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(payload.Session, &session); err == nil {
			return strings.TrimSpace(session.Model), nil
		}
	}
	return "", nil
}

func gatewayLiteMultipartModel(body []byte, boundary string) string {
	if len(body) == 0 || strings.TrimSpace(boundary) == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			return ""
		}
		name := strings.TrimSpace(part.FormName())
		if name != "model" && name != "session" {
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, 1<<20))
		if err != nil {
			return ""
		}
		if name == "model" {
			return strings.TrimSpace(string(value))
		}
		var session struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(value, &session) == nil && strings.TrimSpace(session.Model) != "" {
			return strings.TrimSpace(session.Model)
		}
	}
}

func gatewayLiteModelFromPath(path string) string {
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if !strings.EqualFold(part, "models") || i+1 >= len(parts) {
			continue
		}
		model := parts[i+1]
		if model == "" {
			return ""
		}
		if action := strings.IndexByte(model, ':'); action >= 0 {
			model = model[:action]
		}
		return strings.TrimSpace(model)
	}
	return ""
}

// GatewayModelPermissionError 共用 HTTP 准入与 Responses 长连接逐轮权限。
// 模型映射只影响执行，不得提升客户模型权限；空模型仅供无正文的握手/任务查询，
// response.create 必须由处理器先解析显式模型或继承上轮模型后再调用。
func GatewayModelPermissionError(apiKey *service.APIKey, model string) (string, string) {
	if apiKey == nil {
		return "INVALID_API_KEY", "Invalid API key"
	}
	if !gatewayLiteModelAllowed(model, apiKey.AllowedModels) {
		return "MODEL_NOT_ALLOWED", "This model is not allowed by your current plan"
	}
	if strings.TrimSpace(model) != "" && apiKey.Group != nil &&
		(!apiKey.Group.ModelAllowlist.Allows(model) || !apiKey.Group.IsModelAllowedByCustomList(model)) {
		return "GROUP_MODEL_NOT_ALLOWED", "This model is not allowed by the current gateway group"
	}
	return "", ""
}

func gatewayLiteModelAllowed(model string, allowedModels []string) bool {
	model = strings.TrimSpace(model)
	if model == "" || len(allowedModels) == 0 {
		return true
	}
	for _, pattern := range allowedModels {
		if gatewayLiteModelPatternMatch(model, pattern) {
			return true
		}
	}
	return false
}

func gatewayLiteModelPatternMatch(model, pattern string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if pattern == "*" || model == pattern {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	cursor := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		pos := strings.Index(model[cursor:], part)
		if pos < 0 {
			return false
		}
		if i == 0 && !strings.HasPrefix(pattern, "*") && pos != 0 {
			return false
		}
		cursor += pos + len(part)
	}
	if !strings.HasSuffix(pattern, "*") && len(parts) > 0 {
		last := parts[len(parts)-1]
		return last == "" || strings.HasSuffix(model, last)
	}
	return true
}

func gatewayLiteCurrentGatewayID(region, gatewayCode string) string {
	if strings.TrimSpace(gatewayCode) != "" {
		return strings.TrimSpace(gatewayCode)
	}
	return strings.TrimSpace(region)
}

func extractGatewayLiteAPIKey(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	if key := strings.TrimSpace(c.GetHeader("x-api-key")); key != "" {
		return key
	}
	return strings.TrimSpace(c.GetHeader("x-goog-api-key"))
}

func splitGatewayLiteAPIKey(raw string) (keyID string, secret string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	parts := strings.Split(raw, "_")
	if len(parts) < 4 || !isSupportedMainSiteAPIKeyBrand(parts[0]) || parts[1] != "sk" {
		return "", "", false
	}
	keyID = strings.Join(parts[2:len(parts)-1], "_")
	secret = parts[len(parts)-1]
	return keyID, secret, keyID != "" && secret != ""
}

func isSupportedMainSiteAPIKeyBrand(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tokenav", "tokeav", "aiceo", "fm":
		return true
	default:
		return false
	}
}

func gatewayLiteSecretMatches(secret, expectedHash string) bool {
	sum := sha256.Sum256([]byte(secret))
	actual := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(expectedHash))) == 1
}

func gatewayLiteResolveKey(ctx context.Context, control GatewayLiteControlPlane, cache *gatewaylite.RedisKeyCache, keyID, region, gatewayCode string, billingExempt, skipUsageTouch bool) (*gatewaylite.KeySnapshot, error) {
	resolveGroupKey := strings.Join([]string{cache.Prefix(), keyID, region, gatewayCode, boolString(billingExempt), boolString(skipUsageTouch)}, "\x00")
	resolvedForThisRequest := false
	value, err, _ := gatewayLiteResolveKeyGroup.Do(resolveGroupKey, func() (any, error) {
		resolvedForThisRequest = true
		// 命中 Redis 也合并同一时刻的并发读取；普通 Key 保持原缓存语义。
		if cached, ok, err := cache.GetImmutable(ctx, keyID, region); err != nil {
			return nil, err
		} else if ok {
			return cached, nil
		}
		fillToken, err := cache.BeginFill(ctx, region)
		if err != nil {
			return nil, err
		}
		key, err := gatewayLiteResolveKeyLive(ctx, control, keyID, region, gatewayCode, billingExempt, skipUsageTouch)
		if err != nil {
			return nil, err
		}
		if key.Active() {
			// Agent 回填由缓存层按用途拒绝；其它 Key 的失效代次门禁保持原样。
			if _, err := cache.SetIfGenerationUnchanged(ctx, *key, region, fillToken); err != nil {
				return nil, err
			}
		}
		return key, nil
	})
	if err != nil {
		return nil, err
	}
	key, ok := value.(*gatewaylite.KeySnapshot)
	if !ok || key == nil {
		return nil, errors.New("control plane returned invalid key snapshot")
	}
	// 首次识别之前仍可合并普通 Key 的缓存读取，但 Agent 请求不能接受
	// 另一请求在撤销前取到的在途结果。每个等待者用自己的上下文真实回源。
	if key.TokenUse == "agent_api_key" && !resolvedForThisRequest {
		return gatewayLiteResolveKeyLive(ctx, control, keyID, region, gatewayCode, billingExempt, skipUsageTouch)
	}
	return key, nil
}

func gatewayLiteResolveKeyLive(ctx context.Context, control GatewayLiteControlPlane, keyID, region, gatewayCode string, billingExempt, skipUsageTouch bool) (*gatewaylite.KeySnapshot, error) {
	resp, err := control.ResolveKey(ctx, gatewaylite.ResolveKeyRequest{
		KeyID: keyID, Region: region, GatewayCode: gatewayCode,
		BillingExempt: billingExempt, SkipUsageTouch: skipUsageTouch,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.OK {
		code := ""
		if resp != nil {
			code = resp.Error
		}
		return nil, gatewayLiteResolveRejectedError{Code: code}
	}
	return &resp.Key, nil
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

type gatewayLiteResolveRejectedError struct {
	Code string
}

type gatewayLiteQuotaRejectedError struct {
	Code   string
	Detail string
}

func (e gatewayLiteQuotaRejectedError) Error() string {
	code := strings.TrimSpace(e.Code)
	detail := strings.TrimSpace(e.Detail)
	if code == "" && detail == "" {
		return "control plane rejected quota"
	}
	if detail == "" {
		return "control plane rejected quota: " + code
	}
	return "control plane rejected quota: " + code + ":" + detail
}

func gatewayLiteQuotaErrorMessage(detail string) string {
	switch strings.TrimSpace(detail) {
	case "period_quota":
		return "Insufficient quota: plan period quota exceeded"
	case "window_5h":
		return "Insufficient quota: current 5-hour period limit exceeded"
	case "window_day":
		return "Insufficient quota: current daily period limit exceeded"
	case "window_week":
		return "Insufficient quota: current 7-day period limit exceeded"
	case "lease_unallocated":
		return "Insufficient quota: no allocatable lease quota available"
	default:
		return "Insufficient quota"
	}
}

func (e gatewayLiteResolveRejectedError) Error() string {
	if strings.TrimSpace(e.Code) == "" {
		return "control plane rejected key"
	}
	return "control plane rejected key: " + e.Code
}

func abortGatewayLiteResolveRejected(c *gin.Context, code string) {
	switch strings.TrimSpace(code) {
	case "insufficient_balance":
		AbortWithError(c, 429, "INSUFFICIENT_QUOTA", "Insufficient quota")
	case "subscription_required":
		AbortWithError(c, 403, "SUBSCRIPTION_REQUIRED", "An active subscription is required")
	case "phone_verification_required":
		AbortWithError(c, 403, "PHONE_VERIFICATION_REQUIRED", "Phone verification is required")
	case "linux_do_binding_required":
		AbortWithError(c, 403, "LINUX_DO_BINDING_REQUIRED", "Linux.do account binding is required")
	case "github_binding_required":
		AbortWithError(c, 403, "GITHUB_BINDING_REQUIRED", "GitHub account binding is required")
	case "fixed_group_required":
		AbortWithError(c, 403, "FIXED_GROUP_REQUIRED", "This API key must be bound to a gateway group")
	default:
		AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
	}
}

func gatewayLiteQuotaCodeIsAccessRejection(code string) bool {
	switch strings.TrimSpace(code) {
	case "subscription_required", "phone_verification_required", "linux_do_binding_required", "github_binding_required":
		return true
	default:
		return false
	}
}

func gatewayLiteReserveQuota(ctx context.Context, control GatewayLiteControlPlane, quota *gatewaylite.RedisQuota, key gatewaylite.KeySnapshot, region, requestID string, estimatedCents int64) (*gatewaylite.LeaseSnapshot, error) {
	if estimatedCents <= 0 {
		estimatedCents = gatewayLiteDefaultLeaseEstimateCents
	}
	localLease, _, reserved, err := quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
		RequestID:          requestID,
		KeyID:              key.KeyID,
		UserID:             key.UserID,
		OrganizationID:     key.OrganizationID,
		BillingSubjectType: key.BillingSubjectType,
		BillingSubjectID:   key.BillingSubjectID,
		Region:             region,
		EstimatedCostCents: estimatedCents,
	})
	if err != nil {
		return nil, err
	}
	if reserved {
		gatewayLiteMaybePrefetchLease(control, quota, key, region, localLease, estimatedCents)
		return &localLease, nil
	}

	// Redis 不可用时无法安全地让等待请求分别做原子预留，因此沿用单请求
	// control-plane 兜底。生产 gateway-lite 必须启用 Redis 才能获得并发合并。
	if quota == nil || !quota.Enabled() {
		return gatewayLiteRefreshLease(ctx, control, quota, key, region, estimatedCents)
	}

	// 只共享“刷新租约”这一慢操作。共享调用结束后，每个请求仍使用自己的
	// request_id 回到 Redis 原子预留，绝不能共享某一个请求的金额记录。
	refreshKey := gatewayLiteLeaseRefreshKey(quota, key.LeaseSubject(), region, estimatedCents)
	for attempt := 0; attempt < gatewayLiteLeaseRefreshAttempts; attempt++ {
		err = gatewayLiteJoinLeaseRefresh(ctx, refreshKey, func(refreshCtx context.Context) error {
			_, refreshErr := gatewayLiteRefreshLease(refreshCtx, control, quota, key, region, estimatedCents)
			return refreshErr
		})
		if err != nil {
			// 若本请求恰好加入了一次失败的低水位预取，允许它再发起一轮
			// 同步续租；正常同步续租的错误不重试，避免重复打主站。
			if errors.Is(err, errGatewayLiteLeasePrefetchIncomplete) && attempt+1 < gatewayLiteLeaseRefreshAttempts {
				continue
			}
			return nil, err
		}
		localLease, _, reserved, err = quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
			RequestID:          requestID,
			KeyID:              key.KeyID,
			UserID:             key.UserID,
			OrganizationID:     key.OrganizationID,
			BillingSubjectType: key.BillingSubjectType,
			BillingSubjectID:   key.BillingSubjectID,
			Region:             region,
			EstimatedCostCents: estimatedCents,
		})
		if err != nil {
			return nil, err
		}
		if reserved {
			gatewayLiteMaybePrefetchLease(control, quota, key, region, localLease, estimatedCents)
			return &localLease, nil
		}
		// 共享续租返回时至少存在本请求对应的预估金额空间；这里再次耗尽只可能是
		// 同批其他请求已经完成了各自的原子预留。按额度不足拒绝即可，
		// 不能再制造第二波主站续租请求。
		return nil, gatewayLiteQuotaRejectedError{Code: "usage_limit_exceeded", Detail: "lease"}
	}
	return nil, errGatewayLiteLeasePrefetchIncomplete
}

func gatewayLiteLeaseRefreshKey(quota *gatewaylite.RedisQuota, subject gatewaylite.LeaseSubject, region string, estimatedCents int64) string {
	prefix := ""
	if quota != nil {
		prefix = quota.Prefix()
	}
	return prefix + ":" + strconv.FormatInt(subject.OrganizationID, 10) + ":" + strconv.FormatInt(subject.UserID, 10) + ":" + strings.TrimSpace(region) + ":" + strconv.FormatInt(estimatedCents, 10)
}

// gatewayLiteJoinLeaseRefresh 使用独立的短超时执行共享调用。等待者取消只
// 结束自己的等待，不会把其他仍然有效的请求正在依赖的续租一起取消。
func gatewayLiteJoinLeaseRefresh(ctx context.Context, refreshKey string, refresh func(context.Context) error) error {
	result := gatewayLiteLeaseRefreshGroup.DoChan(refreshKey, func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.Background(), gatewayLiteLeaseRefreshTimeout)
		defer cancel()
		return nil, refresh(refreshCtx)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case shared := <-result:
		return shared.Err
	}
}

// gatewayLiteRefreshLease 只把主站权威租约安装进 Redis，不替任何具体
// request_id 预留金额；这样 singleflight（并发合并）的结果可以安全共享。
func gatewayLiteRefreshLease(ctx context.Context, control GatewayLiteControlPlane, quota *gatewaylite.RedisQuota, key gatewaylite.KeySnapshot, region string, estimatedCents int64) (*gatewaylite.LeaseSnapshot, error) {
	if estimatedCents <= 0 {
		estimatedCents = gatewayLiteDefaultLeaseEstimateCents
	}
	if control == nil {
		return nil, errors.New("control plane is unavailable")
	}
	observation := gatewaylite.LeaseObservation{}
	if quota != nil && quota.Enabled() {
		if local, found, err := quota.LoadLeaseForSubject(ctx, key.LeaseSubject(), region); err != nil {
			return nil, err
		} else if found {
			observation = local.Observation(true)
		}
	}
	acquire, err := control.AcquireLease(ctx, gatewaylite.AcquireLeaseRequest{
		KeyID:              key.KeyID,
		UserID:             key.UserID,
		OrganizationID:     key.OrganizationID,
		BillingSubjectType: key.BillingSubjectType,
		BillingSubjectID:   key.BillingSubjectID,
		Region:             region,
		GatewayCode:        gatewayLiteCurrentGatewayID(region, ""),
		EstimatedCostCents: estimatedCents,
		Reason:             "gateway_request",
		LeaseObservation:   observation,
	})
	if err != nil {
		return nil, err
	}
	acquireRejected := gatewayLiteQuotaRejectedError{}
	if acquire != nil && acquire.OK {
		installed, usable, installErr := gatewayLiteApplyLeaseSnapshot(ctx, quota, key.LeaseSubject(), region, acquire.Lease, estimatedCents)
		if installErr != nil {
			return nil, installErr
		}
		if usable {
			return installed, nil
		}
	} else {
		if acquire == nil || strings.TrimSpace(acquire.Error) == "" {
			return nil, errors.New("control plane returned an empty lease response")
		}
		acquireRejected = gatewayLiteQuotaRejectedError{Code: acquire.Error, Detail: acquire.ErrorDetail}
		if gatewayLiteQuotaCodeIsAccessRejection(acquireRejected.Code) || acquireRejected.Code == "lease_state_conflict" {
			return nil, acquireRejected
		}
	}

	// 主站快照可能尚未包含 Redis 中更新的 reserved/spent（预留/已用）。
	// 带上最新观察只执行一次强制重平衡，避免重复发放同一笔额度。
	observation = gatewaylite.LeaseObservation{}
	if quota != nil && quota.Enabled() {
		if latest, found, loadErr := quota.LoadLeaseForSubject(ctx, key.LeaseSubject(), region); loadErr != nil {
			return nil, loadErr
		} else if found {
			observation = latest.Observation(true)
		}
	}
	rebalance, err := control.RebalanceLease(ctx, gatewaylite.RebalanceLeaseRequest{
		KeyID:              key.KeyID,
		UserID:             key.UserID,
		OrganizationID:     key.OrganizationID,
		BillingSubjectType: key.BillingSubjectType,
		BillingSubjectID:   key.BillingSubjectID,
		Region:             region,
		GatewayCode:        gatewayLiteCurrentGatewayID(region, ""),
		EstimatedCostCents: estimatedCents,
		Reason:             "gateway_local_lease_exhausted",
		LeaseObservation:   observation,
	})
	if err != nil {
		return nil, err
	}
	if rebalance == nil || !rebalance.OK {
		if rebalance != nil && strings.TrimSpace(rebalance.Error) != "" {
			return nil, gatewayLiteQuotaRejectedError{Code: rebalance.Error, Detail: rebalance.ErrorDetail}
		}
		if acquireRejected.Code != "" {
			return nil, acquireRejected
		}
		return nil, errors.New("control plane returned an empty rebalance response")
	}
	installed, usable, err := gatewayLiteApplyLeaseSnapshot(ctx, quota, key.LeaseSubject(), region, rebalance.Lease, estimatedCents)
	if err != nil {
		return nil, err
	}
	if !usable {
		return nil, errors.New("lease remained exhausted after forced rebalance")
	}
	return installed, nil
}

func gatewayLiteApplyLeaseSnapshot(ctx context.Context, quota *gatewaylite.RedisQuota, subject gatewaylite.LeaseSubject, region string, lease gatewaylite.LeaseSnapshot, estimatedCents int64) (*gatewaylite.LeaseSnapshot, bool, error) {
	if estimatedCents <= 0 {
		estimatedCents = gatewayLiteDefaultLeaseEstimateCents
	}
	if lease.UserID == 0 {
		lease.UserID = subject.UserID
	}
	if lease.OrganizationID == 0 {
		lease.OrganizationID = subject.OrganizationID
		lease.BillingSubjectType = subject.BillingSubjectType
		lease.BillingSubjectID = subject.BillingSubjectID
	}
	lease.Region = region
	if quota == nil || !quota.Enabled() {
		usable := !lease.Expired(time.Now()) && lease.AvailableCents() >= estimatedCents
		return &lease, usable, nil
	}
	if err := quota.EnsureLease(ctx, lease); err != nil {
		return nil, false, err
	}
	current, ok, err := quota.LoadLeaseForSubject(ctx, subject, region)
	if err != nil || !ok {
		return nil, false, err
	}
	usable := current.AvailableCents() >= estimatedCents
	return &current, usable, nil
}

func gatewayLiteMaybePrefetchLease(control GatewayLiteControlPlane, quota *gatewaylite.RedisQuota, key gatewaylite.KeySnapshot, region string, lease gatewaylite.LeaseSnapshot, estimatedCents int64) {
	if control == nil || quota == nil || !quota.Enabled() || key.UserID <= 0 || strings.TrimSpace(region) == "" {
		return
	}
	// ReserveActiveLease 已返回原子扣除预留后的真实计数；绝大多数请求无需再
	// HGETALL（读取整份租约）一次，只有跨过低水位线时才争抢补充锁。
	if lease.RefillAtCents <= 0 || lease.AvailableCents() > lease.RefillAtCents {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayLiteFinalizeTimeout)
	locked, err := quota.TryAcquireLeaseRefillLockForSubject(ctx, key.LeaseSubject(), region, gatewayLiteLeaseRefillLockTTL)
	cancel()
	if err != nil || !locked {
		return
	}
	refreshKey := gatewayLiteLeaseRefreshKey(quota, key.LeaseSubject(), region, estimatedCents)
	go func() {
		// 低水位预取和租约耗尽兜底共用同一合并键。若耗尽请求撞上正在
		// 进行的预取，它只等待该结果，不再额外请求一次主站。
		_ = gatewayLiteJoinLeaseRefresh(context.Background(), refreshKey, func(refreshCtx context.Context) error {
			prefetchCtx, prefetchCancel := context.WithTimeout(refreshCtx, gatewayLiteLeasePrefetchTimeout)
			defer prefetchCancel()
			if !gatewayLitePrefetchLease(prefetchCtx, control, quota, key, region, estimatedCents) {
				return errGatewayLiteLeasePrefetchIncomplete
			}
			return nil
		})
	}()
}

func gatewayLitePrefetchLease(ctx context.Context, control GatewayLiteControlPlane, quota *gatewaylite.RedisQuota, key gatewaylite.KeySnapshot, region string, estimatedCents int64) bool {
	refilled, err := gatewaylite.RefillLeaseAtLowWatermarkForSubject(
		ctx,
		control,
		quota,
		key.LeaseSubject(),
		region,
		gatewayLiteCurrentGatewayID(region, ""),
		"gateway_low_watermark_prefetch",
		estimatedCents,
	)
	if err != nil {
		log.Printf("gateway-lite: low-watermark lease refill failed user_id=%d gateway=%s: %v", key.UserID, region, err)
		return false
	}
	return refilled
}

func gatewayLiteSnapshotToAPIKey(rawKey string, snapshot gatewaylite.KeySnapshot) *service.APIKey {
	now := time.Now()
	var compiledIPWhitelist, compiledIPBlacklist *ip.CompiledIPRules
	if len(snapshot.IPWhitelist) > 0 {
		compiledIPWhitelist = ip.CompileIPRules(snapshot.IPWhitelist)
	}
	if len(snapshot.IPBlacklist) > 0 {
		compiledIPBlacklist = ip.CompileIPRules(snapshot.IPBlacklist)
	}
	group := gatewayLiteSnapshotGroupToServiceGroup(snapshot)
	groupID := snapshot.GroupID
	if group != nil && group.ID > 0 {
		// 主站保存的是跨网关唯一的全局分组编号；轻量网关调度器只认识本地分组编号。
		// 分组对象已经通过 local_group_id 完成归一化，运行时 Key 必须沿用这个本地编号。
		groupID = group.ID
	}
	userConcurrency := snapshot.Concurrency
	userRPMLimit := snapshot.RateLimitRPM
	userTPMLimit := snapshot.RateLimitTPM
	if snapshot.OrganizationID > 0 {
		// 企业请求的三层限制必须分别进入原子门禁。兼容字段是三层最小值，
		// 若继续塞进成员层，会让不同 Key 共享本应独立的 Key 上限。
		userConcurrency = snapshot.MemberConcurrency
		userRPMLimit = snapshot.MemberRateLimitRPM
		userTPMLimit = snapshot.MemberRateLimitTPM
	}
	user := &service.User{
		ID:                           snapshot.UserID,
		OrganizationID:               snapshot.OrganizationID,
		OrganizationConcurrency:      snapshot.OrganizationConcurrency,
		OrganizationRPMLimit:         snapshot.OrganizationRateLimitRPM,
		OrganizationTPMLimit:         snapshot.OrganizationRateLimitTPM,
		MemberConcurrency:            snapshot.MemberConcurrency,
		MemberRPMLimit:               snapshot.MemberRateLimitRPM,
		MemberTPMLimit:               snapshot.MemberRateLimitTPM,
		Tier:                         snapshot.Tier,
		Email:                        strings.TrimSpace(snapshot.UserEmail),
		Username:                     strings.TrimSpace(snapshot.Username),
		Role:                         service.RoleUser,
		Status:                       service.StatusActive,
		Concurrency:                  userConcurrency,
		RPMLimit:                     userRPMLimit,
		TPMLimit:                     userTPMLimit,
		UserGroupRPMOverrideResolved: true,
	}
	if snapshot.OrganizationID <= 0 && user.Concurrency <= 0 {
		user.Concurrency = 1
	}
	return &service.APIKey{
		ID:                              0,
		UserID:                          snapshot.UserID,
		Key:                             rawKey,
		Name:                            snapshot.KeyID,
		GroupID:                         &groupID,
		Status:                          service.StatusAPIKeyActive,
		CreatedAt:                       now,
		UpdatedAt:                       now,
		User:                            user,
		Group:                           group,
		AllowedModels:                   append([]string(nil), snapshot.AllowedModels...),
		AllowedKeywordRouteGroupIDs:     append([]int64(nil), snapshot.AllowedKeywordRouteGroupIDs...),
		KeywordRouteGroupAccessEnforced: snapshot.KeywordRouteGroupAccessEnforced,
		GatewayStrategy:                 snapshot.GatewayStrategy,
		KeyDefaultGatewayNodeID:         snapshot.KeyDefaultGatewayNodeID,
		IPWhitelist:                     append([]string(nil), snapshot.IPWhitelist...),
		IPBlacklist:                     append([]string(nil), snapshot.IPBlacklist...),
		CompiledIPWhitelist:             compiledIPWhitelist,
		CompiledIPBlacklist:             compiledIPBlacklist,
		UserRateMultiplier:              cloneOptionalRate(snapshot.UserRateMultiplier),
		UserGroupRateMultipliers:        cloneUserGroupRateMultipliers(snapshot.UserGroupRateMultipliers),
		KeyConcurrencyLimit:             snapshot.KeyConcurrency,
		KeyRPMLimit:                     snapshot.KeyRateLimitRPM,
		KeyTPMLimit:                     snapshot.KeyRateLimitTPM,
	}
}

// GatewayLiteSnapshotToAPIKeyForInternalTask rebuilds the same non-secret
// runtime authorization view used by request authentication. Durable workers
// resolve the KeyID again instead of persisting the raw client key. The empty
// raw key is intentional: image execution needs group/user policy, never the
// customer's bearer secret.
func GatewayLiteSnapshotToAPIKeyForInternalTask(snapshot gatewaylite.KeySnapshot) *service.APIKey {
	return gatewayLiteSnapshotToAPIKey("", snapshot)
}

func cloneOptionalRate(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneUserGroupRateMultipliers(values map[int64]float64) map[int64]float64 {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[int64]float64, len(values))
	for groupID, rate := range values {
		cloned[groupID] = rate
	}
	return cloned
}

func gatewayLiteRemoteAPIKeyIPAllowed(c *gin.Context, apiKey *service.APIKey) bool {
	if apiKey == nil || (len(apiKey.IPWhitelist) == 0 && len(apiKey.IPBlacklist) == 0) {
		return true
	}
	// ForwardedClientIPContext has already frozen the live trusted-proxy policy
	// for this request. Passing false is intentional: the request snapshot, when
	// present, overrides it and prevents a direct client from spoofing headers.
	clientIP := ip.GetSecurityClientIP(c, false)
	allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, apiKey.CompiledIPWhitelist, apiKey.CompiledIPBlacklist)
	return allowed
}

func gatewayLiteSnapshotGroupToServiceGroup(snapshot gatewaylite.KeySnapshot) *service.Group {
	if snapshot.Group != nil && snapshot.Group.ID > 0 {
		group := &service.Group{
			ID:                              snapshot.Group.ID,
			Name:                            snapshot.Group.Name,
			Platform:                        snapshot.Group.Platform,
			Status:                          snapshot.Group.Status,
			IsExclusive:                     snapshot.Group.IsExclusive,
			MinimumTier:                     cloneGatewayLiteSnapshotPtr(snapshot.Group.MinimumTier),
			EffectiveRequiredTier:           snapshot.Group.EffectiveRequiredTier,
			SubscriptionType:                snapshot.Group.SubscriptionType,
			RateMultiplier:                  snapshot.Group.RateMultiplier,
			PeakRateEnabled:                 snapshot.Group.PeakRateEnabled,
			PeakStart:                       snapshot.Group.PeakStart,
			PeakEnd:                         snapshot.Group.PeakEnd,
			PeakRateMultiplier:              snapshot.Group.PeakRateMultiplier,
			PeakTimezone:                    snapshot.Group.PeakTimezone,
			DailyLimitUSD:                   cloneGatewayLiteSnapshotPtr(snapshot.Group.DailyLimitUSD),
			WeeklyLimitUSD:                  cloneGatewayLiteSnapshotPtr(snapshot.Group.WeeklyLimitUSD),
			MonthlyLimitUSD:                 cloneGatewayLiteSnapshotPtr(snapshot.Group.MonthlyLimitUSD),
			AllowImageGeneration:            snapshot.Group.AllowImageGeneration,
			ImageRateIndependent:            snapshot.Group.ImageRateIndependent,
			ImageRateMultiplier:             snapshot.Group.ImageRateMultiplier,
			ImagePrice1K:                    cloneGatewayLiteSnapshotPtr(snapshot.Group.ImagePrice1K),
			ImagePrice2K:                    cloneGatewayLiteSnapshotPtr(snapshot.Group.ImagePrice2K),
			ImagePrice4K:                    cloneGatewayLiteSnapshotPtr(snapshot.Group.ImagePrice4K),
			VideoRateIndependent:            snapshot.Group.VideoRateIndependent,
			VideoRateMultiplier:             snapshot.Group.VideoRateMultiplier,
			VideoPrice480P:                  cloneGatewayLiteSnapshotPtr(snapshot.Group.VideoPrice480P),
			VideoPrice720P:                  cloneGatewayLiteSnapshotPtr(snapshot.Group.VideoPrice720P),
			VideoPrice1080P:                 cloneGatewayLiteSnapshotPtr(snapshot.Group.VideoPrice1080P),
			VideoModelPrices:                service.NormalizeVideoModelPrices(snapshot.Group.VideoModelPrices),
			WebSearchPricePerCall:           cloneGatewayLiteSnapshotPtr(snapshot.Group.WebSearchPricePerCall),
			SearchPricePer1k:                cloneGatewayLiteSnapshotPtr(snapshot.Group.SearchPricePer1k),
			AudioRealtimePricePerMin:        cloneGatewayLiteSnapshotPtr(snapshot.Group.AudioRealtimePricePerMin),
			AudioTTSPricePerMillionChars:    cloneGatewayLiteSnapshotPtr(snapshot.Group.AudioTtsPricePerMillionChars),
			AudioSTTPricePerHour:            cloneGatewayLiteSnapshotPtr(snapshot.Group.AudioSttPricePerHour),
			AllowOpenAILongContextBilling:   snapshot.Group.AllowOpenAILongContextBilling,
			AllowBlockedKeywordRequests:     snapshot.Group.AllowBlockedKeywordRequests,
			ClaudeCodeOnly:                  snapshot.Group.ClaudeCodeOnly,
			FallbackGroupID:                 cloneGatewayLiteSnapshotPtr(snapshot.Group.FallbackGroupID),
			FallbackGroupIDOnInvalidRequest: cloneGatewayLiteSnapshotPtr(snapshot.Group.FallbackGroupIDOnInvalidRequest),
			FailoverGroupID:                 cloneGatewayLiteSnapshotPtr(snapshot.Group.FailoverGroupID),
			FailoverStrategy:                snapshot.Group.FailoverStrategy,
			ModelRouting:                    cloneGatewayLiteModelRouting(snapshot.Group.ModelRouting),
			ModelRoutingEnabled:             snapshot.Group.ModelRoutingEnabled,
			ModelMapping:                    cloneGatewayLiteStringMap(snapshot.Group.ModelMapping),
			FallbackModel:                   snapshot.Group.FallbackModel,
			BillingModelSource:              snapshot.Group.BillingModelSource,
			MCPXMLInject:                    snapshot.Group.MCPXMLInject,
			SupportedModelScopes:            append([]string(nil), snapshot.Group.SupportedModelScopes...),
			AllowMessagesDispatch:           snapshot.Group.AllowMessagesDispatch,
			AllowLive:                       snapshot.Group.AllowLive,
			RequireOAuthOnly:                snapshot.Group.RequireOAuthOnly,
			RequirePrivacySet:               snapshot.Group.RequirePrivacySet,
			DefaultMappedModel:              snapshot.Group.DefaultMappedModel,
			MessagesDispatchModelConfig: service.OpenAIMessagesDispatchModelConfig{
				OpusMappedModel:    snapshot.Group.MessagesDispatchModelConfig.OpusMappedModel,
				SonnetMappedModel:  snapshot.Group.MessagesDispatchModelConfig.SonnetMappedModel,
				HaikuMappedModel:   snapshot.Group.MessagesDispatchModelConfig.HaikuMappedModel,
				ExactModelMappings: cloneGatewayLiteStringMap(snapshot.Group.MessagesDispatchModelConfig.ExactModelMappings),
			},
			ModelsListConfig: service.GroupModelsListConfig{
				Enabled:             snapshot.Group.ModelsListConfig.Enabled,
				Models:              append([]string(nil), snapshot.Group.ModelsListConfig.Models...),
				ResponseModelMode:   snapshot.Group.ModelsListConfig.ResponseModelMode,
				ResponseModelCustom: snapshot.Group.ModelsListConfig.ResponseModelCustom,
			},
			RPMLimit:                snapshot.Group.RPMLimit,
			PerUserConcurrency:      snapshot.Group.PerUserConcurrency,
			MaxReasoningEffort:      snapshot.Group.MaxReasoningEffort,
			ReasoningEffortMappings: gatewayLiteServiceReasoningEffortMappings(snapshot.Group.ReasoningEffortMappings),
			Hydrated:                true,
		}
		gatewayLiteApplySnapshotGroupConfig(group, snapshot.Group.Config)
		gatewayLiteNormalizeSnapshotGroup(group, snapshot)
		return group
	}
	group := &service.Group{
		ID:             snapshot.GroupID,
		Name:           snapshot.GroupName,
		Platform:       snapshot.Platform,
		Status:         service.StatusActive,
		RateMultiplier: 1,
		Hydrated:       true,
	}
	gatewayLiteNormalizeSnapshotGroup(group, snapshot)
	return group
}

func cloneGatewayLiteSnapshotPtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneGatewayLiteModelRouting(values map[string][]int64) map[string][]int64 {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string][]int64, len(values))
	for model, accountIDs := range values {
		cloned[model] = append([]int64(nil), accountIDs...)
	}
	return cloned
}

// cloneGatewayLiteWeightedModelMapping 把快照里的智能分流映射转成 service
// 强类型；认证热路径重建的 Group 必须携带分流配置，否则热路径分流静默失效。
func cloneGatewayLiteWeightedModelMapping(value map[string][]gatewaylite.WeightedModelTarget) map[string][]service.WeightedModelTarget {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string][]service.WeightedModelTarget, len(value))
	for source, targets := range value {
		rows := make([]service.WeightedModelTarget, 0, len(targets))
		for _, target := range targets {
			if strings.TrimSpace(target.Model) == "" {
				continue
			}
			weight := target.Weight
			if weight <= 0 {
				weight = 1
			}
			rows = append(rows, service.WeightedModelTarget{Model: target.Model, Weight: weight})
		}
		if len(rows) > 0 {
			out[source] = rows
		}
	}
	return out
}

func cloneGatewayLiteStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func gatewayLiteNormalizeSnapshotGroup(group *service.Group, snapshot gatewaylite.KeySnapshot) {
	if snapshot.Group != nil && snapshot.Group.LocalGroupID > 0 {
		group.ID = snapshot.Group.LocalGroupID
	}
	if group.ID <= 0 {
		group.ID = snapshot.GroupID
	}
	if group.Name == "" {
		group.Name = snapshot.GroupName
	}
	if group.Platform == "" {
		group.Platform = snapshot.Platform
	}
	if group.Status == "" {
		group.Status = service.StatusActive
	}
	if group.RateMultiplier == 0 {
		group.RateMultiplier = 1
	}
	if group.ImageRateMultiplier == 0 {
		group.ImageRateMultiplier = 1
	}
	if group.VideoRateMultiplier == 0 {
		group.VideoRateMultiplier = 1
	}
	group.PeakRateEnabled, group.PeakStart, group.PeakEnd, group.PeakRateMultiplier, group.PeakTimezone = service.NormalizePeakRateConfig(
		group.PeakRateEnabled, group.PeakStart, group.PeakEnd, group.PeakRateMultiplier, group.PeakTimezone,
	)
}

func gatewayLiteRequestMeta(requestID, keyID string, userID int64, leaseID, region, gatewayCode string, group *service.Group, userRateMultiplier *float64, settlementMode gatewaylite.BillingSettlementMode, requestStartedAt time.Time) gatewaylite.RequestMeta {
	meta := gatewaylite.RequestMeta{
		RequestID:             requestID,
		KeyID:                 keyID,
		UserID:                userID,
		LeaseID:               leaseID,
		Region:                region,
		GatewayID:             gatewayLiteCurrentGatewayID(region, gatewayCode),
		GroupID:               gatewayLiteGroupID(group),
		GroupName:             gatewayLiteGroupName(group),
		RateMultiplier:        gatewayLiteGroupRateMultiplier(group),
		AccountRateMultiplier: 1,
		BillingSettlementMode: settlementMode,
	}
	if requestStartedAt.IsZero() {
		requestStartedAt = time.Now()
	}
	// userRateMultiplier 来自主站 KeySnapshot，且只对应当前实际分组；
	// 上游账号采购倍率不在参数中，因此不可能混入客户销售价。
	if policy, err := service.ResolveEffectiveRate(group, userRateMultiplier, requestStartedAt); err == nil {
		meta.RateMultiplier = policy.EffectiveRateMultiplier
		meta.RatePolicyAudit = &policy
	}
	stamp := gatewaylite.EffectivePolicyAuditForGroup(meta.GroupID)
	meta.PolicyRevision = stamp.Revision
	meta.PolicyChecksum = stamp.Checksum
	meta.PriceVersion = stamp.PriceVersion
	return meta
}

func gatewayLiteGroupID(group *service.Group) int64 {
	if group == nil {
		return 0
	}
	return group.ID
}

func gatewayLiteGroupName(group *service.Group) string {
	if group == nil {
		return ""
	}
	return strings.TrimSpace(group.Name)
}

func gatewayLiteGroupRateMultiplier(group *service.Group) float64 {
	if group == nil || group.RateMultiplier <= 0 {
		return 1
	}
	return group.RateMultiplier
}

func gatewayLiteApplySnapshotGroupConfig(group *service.Group, config map[string]any) {
	if group == nil || len(config) == 0 {
		return
	}
	gatewayLiteSnapshotConfigValue(config, "is_exclusive", &group.IsExclusive)
	gatewayLiteSnapshotConfigValue(config, "subscription_type", &group.SubscriptionType)
	gatewayLiteSnapshotConfigValue(config, "rate_multiplier", &group.RateMultiplier)
	gatewayLiteSnapshotConfigValue(config, "peak_rate_enabled", &group.PeakRateEnabled)
	gatewayLiteSnapshotConfigValue(config, "peak_start", &group.PeakStart)
	gatewayLiteSnapshotConfigValue(config, "peak_end", &group.PeakEnd)
	gatewayLiteSnapshotConfigValue(config, "peak_rate_multiplier", &group.PeakRateMultiplier)
	gatewayLiteSnapshotConfigValue(config, "peak_timezone", &group.PeakTimezone)
	gatewayLiteSnapshotConfigValue(config, "daily_limit_usd", &group.DailyLimitUSD)
	gatewayLiteSnapshotConfigValue(config, "weekly_limit_usd", &group.WeeklyLimitUSD)
	gatewayLiteSnapshotConfigValue(config, "monthly_limit_usd", &group.MonthlyLimitUSD)
	gatewayLiteSnapshotConfigValue(config, "allow_image_generation", &group.AllowImageGeneration)
	gatewayLiteSnapshotConfigValue(config, "image_rate_independent", &group.ImageRateIndependent)
	gatewayLiteSnapshotConfigValue(config, "image_rate_multiplier", &group.ImageRateMultiplier)
	gatewayLiteSnapshotConfigValue(config, "image_price_1k", &group.ImagePrice1K)
	gatewayLiteSnapshotConfigValue(config, "image_price_2k", &group.ImagePrice2K)
	gatewayLiteSnapshotConfigValue(config, "image_price_4k", &group.ImagePrice4K)
	gatewayLiteSnapshotConfigValue(config, "video_rate_independent", &group.VideoRateIndependent)
	gatewayLiteSnapshotConfigValue(config, "video_rate_multiplier", &group.VideoRateMultiplier)
	gatewayLiteSnapshotConfigValue(config, "video_price_480p", &group.VideoPrice480P)
	gatewayLiteSnapshotConfigValue(config, "video_price_720p", &group.VideoPrice720P)
	gatewayLiteSnapshotConfigValue(config, "video_price_1080p", &group.VideoPrice1080P)
	gatewayLiteSnapshotConfigValue(config, "video_model_prices", &group.VideoModelPrices)
	gatewayLiteSnapshotConfigValue(config, "web_search_price_per_call", &group.WebSearchPricePerCall)
	gatewayLiteSnapshotConfigValue(config, "search_price_per_1k", &group.SearchPricePer1k)
	gatewayLiteSnapshotConfigValue(config, "audio_realtime_price_per_min", &group.AudioRealtimePricePerMin)
	gatewayLiteSnapshotConfigValue(config, "audio_tts_price_per_million_chars", &group.AudioTTSPricePerMillionChars)
	gatewayLiteSnapshotConfigValue(config, "audio_stt_price_per_hour", &group.AudioSTTPricePerHour)
	gatewayLiteSnapshotConfigValue(config, "allow_blocked_keyword_requests", &group.AllowBlockedKeywordRequests)
	gatewayLiteSnapshotConfigValue(config, "allow_openai_long_context_billing", &group.AllowOpenAILongContextBilling)
	gatewayLiteSnapshotConfigValue(config, "claude_code_only", &group.ClaudeCodeOnly)
	gatewayLiteSnapshotConfigValue(config, "fallback_group_id", &group.FallbackGroupID)
	gatewayLiteSnapshotConfigValue(config, "fallback_group_id_on_invalid_request", &group.FallbackGroupIDOnInvalidRequest)
	gatewayLiteSnapshotConfigValue(config, "failover_group_id", &group.FailoverGroupID)
	gatewayLiteSnapshotConfigValue(config, "failover_strategy", &group.FailoverStrategy)
	gatewayLiteSnapshotConfigValue(config, "model_routing", &group.ModelRouting)
	gatewayLiteSnapshotConfigValue(config, "model_routing_enabled", &group.ModelRoutingEnabled)
	gatewayLiteSnapshotConfigValue(config, "model_mapping", &group.ModelMapping)
	gatewayLiteSnapshotConfigValue(config, "fallback_model", &group.FallbackModel)
	gatewayLiteSnapshotConfigValue(config, "billing_model_source", &group.BillingModelSource)
	gatewayLiteSnapshotConfigValue(config, "mcp_xml_inject", &group.MCPXMLInject)
	gatewayLiteSnapshotConfigValue(config, "supported_model_scopes", &group.SupportedModelScopes)
	gatewayLiteSnapshotConfigValue(config, "allow_messages_dispatch", &group.AllowMessagesDispatch)
	gatewayLiteSnapshotConfigValue(config, "allow_live", &group.AllowLive)
	gatewayLiteSnapshotConfigValue(config, "require_oauth_only", &group.RequireOAuthOnly)
	gatewayLiteSnapshotConfigValue(config, "require_privacy_set", &group.RequirePrivacySet)
	gatewayLiteSnapshotConfigValue(config, "default_mapped_model", &group.DefaultMappedModel)
	gatewayLiteSnapshotConfigValue(config, "messages_dispatch_model_config", &group.MessagesDispatchModelConfig)
	gatewayLiteSnapshotConfigValue(config, "models_list_config", &group.ModelsListConfig)
	gatewayLiteSnapshotConfigValue(config, "rpm_limit", &group.RPMLimit)
	gatewayLiteSnapshotConfigValue(config, "per_user_concurrency", &group.PerUserConcurrency)
	gatewayLiteSnapshotConfigValue(config, "max_reasoning_effort", &group.MaxReasoningEffort)
	gatewayLiteSnapshotConfigValue(config, "reasoning_effort_mappings", &group.ReasoningEffortMappings)
}

func gatewayLiteServiceReasoningEffortMappings(values []gatewaylite.ReasoningEffortMapping) []service.ReasoningEffortMapping {
	if len(values) == 0 {
		return nil
	}
	out := make([]service.ReasoningEffortMapping, len(values))
	for index := range values {
		out[index] = service.ReasoningEffortMapping{From: values[index].From, To: values[index].To}
	}
	return out
}

func gatewayLiteSnapshotConfigValue[T any](config map[string]any, key string, out *T) {
	raw, ok := config[key]
	if !ok || raw == nil || out == nil {
		return
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return
	}
	_ = json.Unmarshal(payload, out)
}

type gatewayLiteFinalizeInput struct {
	RequestID                 string
	KeyID                     string
	SessionID                 string
	UserID                    int64
	OrganizationID            int64
	BillingSubjectType        string
	BillingSubjectID          int64
	LeaseID                   string
	Region                    string
	GatewayID                 string
	GroupID                   int64
	GroupName                 string
	RateMultiplier            float64
	RatePolicyAudit           *gatewaylite.RatePolicyAudit
	RequestedGroupID          int64
	RequestedGroupName        string
	KeywordPolicyMatched      bool
	KeywordPolicyRerouted     bool
	KeywordPolicySource       string
	KeywordPolicyReason       string
	DetectedLanguages         []string
	RequestedReasoningEffort  string
	ReasoningEffort           string
	Method                    string
	Path                      string
	Model                     string
	Status                    int
	EstimatedCents            int64
	StartedAt                 time.Time
	EndedAt                   time.Time
	AuthLatency               int64
	QuotaLatency              int64
	RoutingLatency            int64
	RoutingBreakdown          *gatewaylite.RoutingLatencyBreakdown
	UpstreamLatency           int64
	ResponseLatency           int64
	Aborted                   bool
	ForceRefund               bool
	BillingSettlementMode     gatewaylite.BillingSettlementMode
	PerTurnSettlement         bool
	AsyncSettlement           bool
	AsyncPersistenceUncertain bool
	UpstreamConsumed          bool
}

type gatewayLitePreDispatchFailureInput struct {
	RequestID          string
	KeyID              string
	UserID             int64
	OrganizationID     int64
	BillingSubjectType string
	BillingSubjectID   int64
	Region             string
	GatewayID          string
	Group              *service.Group
	RateMultiplier     float64
	RatePolicyAudit    *gatewaylite.RatePolicyAudit
	EstimatedCents     int64
	Method             string
	Path               string
	Model              string
	Status             int
	ErrorMessage       string
	StartedAt          time.Time
	AuthLatency        int64
	QuotaLatency       int64
}

func gatewayLiteRequestID(c *gin.Context) string {
	if c != nil && c.Request != nil {
		if requestID, ok := gatewaylite.CanonicalRequestIDFromContext(c.Request.Context()); ok {
			return requestID
		}
	}
	return "generated:" + uuid.New().String()
}

func gatewayLiteFinalizeUsage(reporter gatewaylite.UsageReportClient, quota *gatewaylite.RedisQuota, in gatewayLiteFinalizeInput) {
	if in.Status == 0 {
		in.Status = http.StatusOK
	}
	actualCents := in.EstimatedCents
	if in.PerTurnSettlement && in.BillingSettlementMode == gatewaylite.BillingSettlementModeIdempotentV2 {
		// The connection itself is only a quota holder. Each resp_* turn reports
		// its own charge, so the connection event must never add another cent.
		actualCents = 0
	}
	freeSalesPolicy := in.RatePolicyAudit != nil && in.RatePolicyAudit.Valid() && in.RatePolicyAudit.EffectiveRateMultiplier == 0
	// 0x is only the frozen Token sales policy. Independent image/video billing
	// may still produce a non-zero correction for the same request. Commit zero
	// instead of refunding the request: Redis will release the one-cent admission
	// placeholder when no correction exists, or commit/accept a media correction
	// regardless of whether that correction arrives before or after finalize.
	if freeSalesPolicy {
		actualCents = 0
	}
	if in.AsyncSettlement && in.AsyncPersistenceUncertain {
		// The insert may have committed despite a lost acknowledgement. Preserve
		// its reservation even though the public response is a retryable error.
		return
	}
	shouldRefund := in.ForceRefund || (!in.UpstreamConsumed && (in.Aborted || in.Status >= http.StatusBadRequest))
	if in.PerTurnSettlement && in.BillingSettlementMode == gatewaylite.BillingSettlementModeIdempotentV2 {
		// A Responses WebSocket can finish with an error after earlier resp_* turns
		// were already delivered and billed. Refunding the connection reservation
		// would erase those completed turns from the local lease while the control
		// plane still charges them. Commit the child aggregate instead; the base
		// event itself remains zero-cost through SuppressBaseCharge.
		shouldRefund = false
	}
	if in.AsyncSettlement && !shouldRefund {
		// The submit handler has already persisted all facts needed for recovery.
		// Keep the Redis reservation open and do not emit a fake 202 usage row;
		// the worker's final correction owns both commit and customer billing.
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), gatewayLiteFinalizeTimeout)
	defer cancel()

	if shouldRefund {
		if err := quota.Refund(ctx, in.RequestID); err != nil {
			log.Printf("gateway-lite: failed to refund quota request_id=%s: %v", in.RequestID, err)
		}
		actualCents = 0
	} else if commitReporter, ok := reporter.(gatewaylite.UsageCommitReporter); ok && quota != nil && quota.Enabled() {
		// 正常成功请求把“额度提交 + 用量持久入队”合并成一次 Redis 脚本，
		// 仍然在返回客户端前完成持久交接，但避免三次串行网络往返。
		event := gatewayLiteUsageEvent(in, actualCents)
		resolvedActualCents, err := commitReporter.CommitAndReportUsage(ctx, quota, event)
		if err == nil {
			_ = resolvedActualCents
			return
		}
		// 脚本失败后回到原有幂等路径；即使 Redis 已完成部分操作，
		// request_id 也会阻止主站重复扣费。
		log.Printf("gateway-lite: atomic usage finalize failed request_id=%s: %v", in.RequestID, err)
		resolvedActualCents, commitErr := quota.CommitResolved(ctx, gatewaylite.UsageCommit{RequestID: in.RequestID, ActualCents: actualCents})
		if commitErr != nil {
			log.Printf("gateway-lite: failed to commit quota request_id=%s: %v", in.RequestID, commitErr)
		} else {
			actualCents = resolvedActualCents
		}
	} else {
		resolvedActualCents, err := quota.CommitResolved(ctx, gatewaylite.UsageCommit{RequestID: in.RequestID, ActualCents: actualCents})
		if err != nil {
			log.Printf("gateway-lite: failed to commit quota request_id=%s: %v", in.RequestID, err)
		} else {
			actualCents = resolvedActualCents
		}
	}

	event := gatewayLiteUsageEvent(in, actualCents)
	gatewayLiteEnqueueUsageEvent(reporter, event)
}

func gatewayLiteUsageEvent(in gatewayLiteFinalizeInput, actualCents int64) gatewaylite.UsageEvent {
	event := gatewaylite.UsageEvent{
		RequestID:                 in.RequestID,
		UserID:                    in.UserID,
		OrganizationID:            in.OrganizationID,
		BillingSubjectType:        strings.TrimSpace(in.BillingSubjectType),
		BillingSubjectID:          in.BillingSubjectID,
		KeyID:                     in.KeyID,
		SessionID:                 in.SessionID,
		LeaseID:                   in.LeaseID,
		Region:                    in.Region,
		GatewayID:                 gatewayLiteCurrentGatewayID(in.Region, in.GatewayID),
		GatewayGroupID:            in.GroupID,
		GatewayGroupName:          in.GroupName,
		RequestedGatewayGroupID:   in.RequestedGroupID,
		RequestedGatewayGroupName: in.RequestedGroupName,
		KeywordPolicyMatched:      in.KeywordPolicyMatched,
		KeywordPolicyRerouted:     in.KeywordPolicyRerouted,
		KeywordPolicySource:       in.KeywordPolicySource,
		KeywordPolicyReason:       in.KeywordPolicyReason,
		DetectedLanguages:         append([]string(nil), in.DetectedLanguages...),
		RequestedReasoningEffort:  strings.TrimSpace(in.RequestedReasoningEffort),
		ReasoningEffort:           strings.TrimSpace(in.ReasoningEffort),
		RateMultiplier:            in.RateMultiplier,
		Protocol:                  "openai_compatible",
		Method:                    in.Method,
		Path:                      in.Path,
		Model:                     in.Model,
		Status:                    in.Status,
		EstimatedCents:            in.EstimatedCents,
		ActualCents:               actualCents,
		SettlementMode:            string(in.BillingSettlementMode),
		SuppressBaseCharge:        in.PerTurnSettlement && in.BillingSettlementMode == gatewaylite.BillingSettlementModeIdempotentV2,
		LatencyMillis:             in.EndedAt.Sub(in.StartedAt).Milliseconds(),
		AuthLatencyMillis:         gatewayLiteNonNegativeLatency(in.AuthLatency),
		QuotaLatencyMillis:        gatewayLiteNonNegativeLatency(in.QuotaLatency),
		RoutingLatencyMillis:      gatewayLiteNonNegativeLatency(in.RoutingLatency),
		RoutingBreakdownMillis:    in.RoutingBreakdown,
		UpstreamLatencyMillis:     gatewayLiteNonNegativeLatency(in.UpstreamLatency),
		ResponseLatencyMillis:     gatewayLiteNonNegativeLatency(in.ResponseLatency),
		StartedAtMillis:           in.StartedAt.UnixMilli(),
		EndedAtMillis:             in.EndedAt.UnixMilli(),
	}
	if in.RatePolicyAudit != nil && in.RatePolicyAudit.Valid() {
		policy := in.RatePolicyAudit.Clone()
		event.RatePolicyAudit = &policy
	}
	if event.SuppressBaseCharge {
		// CommitAndReportUsage may return a non-zero connection aggregate for
		// local lease accounting. That aggregate is never a second user charge.
		event.ActualCents = 0
	}
	policyAudit := gatewaylite.EffectivePolicyAuditForGroup(in.GroupID)
	event.PolicyRevision = policyAudit.Revision
	event.PolicyChecksum = policyAudit.Checksum
	event.PriceVersion = policyAudit.PriceVersion
	return event
}

func gatewayLiteReportPreDispatchFailure(reporter gatewaylite.UsageReportClient, in gatewayLitePreDispatchFailureInput) {
	if reporter == nil || in.RequestID == "" || in.UserID <= 0 {
		return
	}
	if in.Status == 0 {
		in.Status = http.StatusServiceUnavailable
	}
	endedAt := time.Now()
	event := gatewaylite.UsageEvent{
		RequestID:          in.RequestID,
		UserID:             in.UserID,
		OrganizationID:     in.OrganizationID,
		BillingSubjectType: strings.TrimSpace(in.BillingSubjectType),
		BillingSubjectID:   in.BillingSubjectID,
		KeyID:              in.KeyID,
		Region:             in.Region,
		GatewayID:          gatewayLiteCurrentGatewayID(in.Region, in.GatewayID),
		GatewayGroupID:     gatewayLiteGroupID(in.Group),
		GatewayGroupName:   gatewayLiteGroupName(in.Group),
		RateMultiplier:     in.RateMultiplier,
		Protocol:           "openai_compatible",
		Method:             in.Method,
		Path:               in.Path,
		Model:              in.Model,
		Status:             in.Status,
		ErrorMessage:       strings.TrimSpace(in.ErrorMessage),
		EstimatedCents:     in.EstimatedCents,
		ActualCents:        0,
		LatencyMillis:      endedAt.Sub(in.StartedAt).Milliseconds(),
		AuthLatencyMillis:  gatewayLiteNonNegativeLatency(in.AuthLatency),
		QuotaLatencyMillis: gatewayLiteNonNegativeLatency(in.QuotaLatency),
		StartedAtMillis:    in.StartedAt.UnixMilli(),
		EndedAtMillis:      endedAt.UnixMilli(),
	}
	if event.RateMultiplier <= 0 {
		event.RateMultiplier = gatewayLiteGroupRateMultiplier(in.Group)
	}
	if event.EstimatedCents <= 0 {
		event.EstimatedCents = gatewayLiteDefaultLeaseEstimateCents
	}
	if in.RatePolicyAudit != nil && in.RatePolicyAudit.Valid() {
		policy := in.RatePolicyAudit.Clone()
		event.RatePolicyAudit = &policy
		event.RateMultiplier = policy.EffectiveRateMultiplier
	}
	policyAudit := gatewaylite.EffectivePolicyAuditForGroup(event.GatewayGroupID)
	event.PolicyRevision = policyAudit.Revision
	event.PolicyChecksum = policyAudit.Checksum
	event.PriceVersion = policyAudit.PriceVersion
	gatewayLiteEnqueueUsageEvent(reporter, event)
}

func gatewayLiteEnqueueUsageEvent(reporter gatewaylite.UsageReportClient, event gatewaylite.UsageEvent) {
	if reporter == nil {
		return
	}
	// Redis XADD 是成功请求与持久计费队列之间的交接点，必须在中间件返回前完成。
	// 这里只做一次本地持久入队，不等待主站结算；不能为每个请求创建无人等待的 goroutine，
	// 否则高并发或进程退出时会出现已返回成功、用量却仍停留在进程内存中的窗口。
	reportCtx, cancel := context.WithTimeout(context.Background(), gatewayLiteFinalizeTimeout)
	defer cancel()
	if err := reporter.ReportUsage(reportCtx, event); err != nil {
		log.Printf("gateway-lite: failed to enqueue usage request_id=%s: %v", event.RequestID, err)
	}
}

func gatewayLiteContextLatencyMillis(c *gin.Context, key string) int64 {
	if c == nil || strings.TrimSpace(key) == "" {
		return 0
	}
	value, ok := c.Get(key)
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int64:
		return gatewayLiteNonNegativeLatency(typed)
	case int:
		return gatewayLiteNonNegativeLatency(int64(typed))
	case int32:
		return gatewayLiteNonNegativeLatency(int64(typed))
	case float64:
		return gatewayLiteNonNegativeLatency(int64(typed))
	default:
		return 0
	}
}

func gatewayLiteRoutingLatencyBreakdown(c *gin.Context, total int64) *gatewaylite.RoutingLatencyBreakdown {
	if c == nil {
		return nil
	}
	keys := []string{
		service.OpsRoutingUserSlotLatencyMsKey,
		service.OpsRoutingBillingLatencyMsKey,
		service.OpsRoutingSessionPolicyLatencyMsKey,
		service.OpsRoutingSelectionLatencyMsKey,
		service.OpsRoutingPostAccountSlotLatencyMsKey,
	}
	measured := make([]int64, len(keys))
	seen := false
	sum := int64(0)
	for i, key := range keys {
		if _, ok := c.Get(key); ok {
			seen = true
		}
		measured[i] = gatewayLiteContextLatencyMillis(c, key)
		sum += measured[i]
	}
	if !seen {
		return nil
	}
	other := gatewayLiteNonNegativeLatency(total - sum)
	selection := service.OpsRoutingSelectionBreakdown{}
	if c.Request != nil {
		selection, _ = service.OpsRoutingSelectionBreakdownFromContext(c.Request.Context())
	}
	selectionMeasured := selection.GroupValidation + selection.StickyLookup + selection.AccountResolve +
		selection.AccountSlot + selection.StickyRefresh + selection.CandidateResolution + selection.Settings
	return &gatewaylite.RoutingLatencyBreakdown{
		Version:                  1,
		UserSlot:                 measured[0],
		BillingRecheck:           measured[1],
		SessionPolicy:            measured[2],
		AccountSelection:         measured[3],
		PostSelectionAccountSlot: measured[4],
		SelectionGroupValidation: selection.GroupValidation,
		SelectionStickyLookup:    selection.StickyLookup,
		SelectionAccountResolve:  selection.AccountResolve,
		SelectionAccountSlot:     selection.AccountSlot,
		SelectionStickyRefresh:   selection.StickyRefresh,
		SelectionCandidates:      selection.CandidateResolution,
		SelectionSettings:        selection.Settings,
		SelectionOther:           gatewayLiteNonNegativeLatency(measured[3] - selectionMeasured),
		Other:                    other,
	}
}

func gatewayLiteNonNegativeLatency(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
