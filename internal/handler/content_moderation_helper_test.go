package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestContentModerationErrorCodeUsesStablePolicyReason(t *testing.T) {
	tests := []struct {
		name     string
		decision *service.ContentModerationDecision
		want     string
	}{
		{name: "fallback", decision: nil, want: "content_policy_violation"},
		{name: "language", decision: &service.ContentModerationDecision{Action: service.ContentModerationActionLanguageBlock}, want: "unsupported_language"},
		{name: "invalid request", decision: &service.ContentModerationDecision{Action: service.ContentModerationActionError}, want: "invalid_request_error"},
		{name: "no candidate", decision: &service.ContentModerationDecision{RouteReason: "no_candidate"}, want: "keyword_allowed_group_unavailable"},
		{name: "websocket reconnect", decision: &service.ContentModerationDecision{RouteReason: "websocket_reconnect_required"}, want: "keyword_group_reconnect_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, contentModerationErrorCode(test.decision))
		})
	}
}

func TestRunContentModerationBlockedMarksGatewayLiteReservationForRefund(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)

	reservation := gatewaylite.NewRequestReservation(1, "lease-1")
	requestCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "client:ws-policy-refund", UserID: 7, LeaseID: "lease-1", Reservation: reservation,
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestCtx)
	groupID := int64(10)
	apiKey := &service.APIKey{
		ID: 8, GroupID: &groupID,
		User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "strict", Status: service.StatusActive},
	}

	decision := runContentModeration(
		c, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test",
		[]byte(`{"input":"forbidden request"}`),
	)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Equal(t, "10", c.Writer.Header().Get("X-Gateway-Original-Group-ID"))
	require.Equal(t, "10", c.Writer.Header().Get("X-Gateway-Effective-Group-ID"))
	require.Equal(t, "no_candidate", c.Writer.Header().Get("X-Keyword-Policy-Route"))
	require.NotContains(t, c.Writer.Header().Get("X-Keyword-Policy-Route"), "forbidden")
	meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
	require.True(t, ok)
	require.True(t, meta.KeywordPolicyMatched)
	require.False(t, meta.KeywordPolicyRerouted)
	require.Equal(t, "user", meta.KeywordPolicySource)
	require.Equal(t, "no_candidate", meta.KeywordPolicyReason)
	require.Equal(t, groupID, meta.OriginalGroupID)
	require.Equal(t, groupID, meta.GroupID)
	require.NotEmpty(t, meta.DetectedLanguages)
	_, _, forceRefund := reservation.Snapshot()
	require.True(t, forceRefund)

	// 第一次拒绝后，第三方客户端会继续携带本地保存的旧消息。handler 必须
	// 把 service 返回的隔离副本交给后续模型映射和上游转发，而不是只用它审查。
	nextC, _ := gin.CreateTestContext(httptest.NewRecorder())
	nextC.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	nextBody := []byte(`{"input":[
		{"role":"user","content":[{"type":"input_text","text":"forbidden request"}]},
		{"role":"assistant","content":[{"type":"output_text","text":"client error"}]},
		{"role":"user","content":[{"type":"input_text","text":"sorry, continue"}]}
	]}`)
	nextDecision := runContentModeration(
		nextC, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test", nextBody,
	)
	require.NotNil(t, nextDecision)
	require.True(t, nextDecision.Allowed)
	forwardBody := contentModerationForwardBody(nextC, nextBody)
	require.NotContains(t, string(forwardBody), "forbidden request")
	require.NotContains(t, string(forwardBody), "client error")
	require.Contains(t, string(forwardBody), "sorry, continue")
	require.Len(t, gjson.GetBytes(forwardBody, "input").Array(), 1)
}

func TestRunContentModerationForwardsResponsesWithoutRepeatedBlockedInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden directive"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)
	groupID := int64(10)
	apiKey := &service.APIKey{
		ID: 18, GroupID: &groupID,
		User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "strict", Status: service.StatusActive},
	}

	firstC, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstC.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	first := []byte(`{"instructions":"forbidden directive","input":"first safe request"}`)
	decision := runContentModeration(
		firstC, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test", first,
	)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Equal(t, service.ContentModerationSourceInstructions, decision.ContentSource)

	nextC, _ := gin.CreateTestContext(httptest.NewRecorder())
	nextC.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	next := []byte(`{"instructions":"forbidden directive","input":"continue safely"}`)
	nextDecision := runContentModeration(
		nextC, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test", next,
	)
	require.NotNil(t, nextDecision)
	require.True(t, nextDecision.Allowed)
	forwardBody := contentModerationForwardBody(nextC, next)
	require.False(t, gjson.GetBytes(forwardBody, "instructions").Exists())
	require.Equal(t, "continue safely", gjson.GetBytes(forwardBody, "input").String())
}

func TestRunContentModerationUsesStableSnapshotKeyIsolationScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden directive"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)
	groupID := int64(10)
	apiKey := &service.APIKey{
		ID: 0, Name: "snapshot-key-fallback", GroupID: &groupID,
		User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "strict", Status: service.StatusActive},
	}
	requestContext := func(keyID, requestID string) context.Context {
		return gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
			KeyID: keyID, RequestID: requestID, UserID: 7,
		})
	}

	firstC, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstC.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestContext("snapshot-key-a", "first"))
	first := []byte(`{"instructions":"forbidden directive","input":"first safe request"}`)
	decision := runContentModeration(firstC, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses, "gpt-test", first)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)

	nextC, _ := gin.CreateTestContext(httptest.NewRecorder())
	nextC.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestContext("snapshot-key-a", "next"))
	next := []byte(`{"instructions":"forbidden directive","input":"continue safely"}`)
	nextDecision := runContentModeration(nextC, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses, "gpt-test", next)
	require.NotNil(t, nextDecision)
	require.True(t, nextDecision.Allowed)
	require.False(t, gjson.GetBytes(contentModerationForwardBody(nextC, next), "instructions").Exists())

	otherC, _ := gin.CreateTestContext(httptest.NewRecorder())
	otherC.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestContext("snapshot-key-b", "other"))
	otherDecision := runContentModeration(otherC, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses, "gpt-test", next)
	require.NotNil(t, otherDecision)
	require.True(t, otherDecision.Blocked, "different control-plane keys must not share isolation fingerprints")
}

func TestRunContentModerationWebSocketLaterTurnRequiresReconnectAndRefunds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)

	reservation := gatewaylite.NewRequestReservation(1, "lease-ws-later")
	requestCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "client:ws-policy-later", UserID: 7, LeaseID: "lease-ws-later", Reservation: reservation,
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil).WithContext(requestCtx)
	c.Set("content_moderation_disable_keyword_reroute", true)
	groupID := int64(10)
	apiKey := &service.APIKey{
		ID: 8, GroupID: &groupID,
		User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "strict", Status: service.StatusActive},
	}

	decision := runContentModeration(
		c, nil, svc, nil, apiKey, middleware.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test",
		[]byte(`{"type":"response.create","model":"gpt-test","input":"forbidden request"}`),
	)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Equal(t, "websocket_reconnect_required", decision.RouteReason)
	require.Equal(t, "keyword_group_reconnect_required", contentModerationErrorCode(decision))
	_, _, forceRefund := reservation.Snapshot()
	require.True(t, forceRefund)

	payload := contentModerationWSErrorPayload(decision)
	require.Equal(t, "keyword_group_reconnect_required", gjson.GetBytes(payload, "error.code").String())
	require.True(t, gjson.GetBytes(payload, "error.retryable").Bool())
	require.Equal(t, "websocket_reconnect_required", gjson.GetBytes(payload, "error.route_reason").String())
	require.NotContains(t, string(payload), "forbidden")
}

type fixedKeywordPolicyGroupResolver struct {
	group        *service.Group
	subscription *service.UserSubscription
}

type rateFreezingKeywordPolicyGroupResolver struct {
	group    *service.Group
	frozenAt time.Time
}

func (r *rateFreezingKeywordPolicyGroupResolver) ResolveBlockedKeywordRequestGroup(context.Context, *service.APIKey, string, string, []byte, bool) (*service.KeywordRouteResult, error) {
	return &service.KeywordRouteResult{Group: r.group}, nil
}

func (r *rateFreezingKeywordPolicyGroupResolver) FreezeRatePolicyForAPIKey(ctx context.Context, apiKey *service.APIKey, at time.Time) (context.Context, error) {
	r.frozenAt = at
	frozen, _, err := service.FreezeEffectiveRatePolicy(ctx, apiKey.Group, apiKey.UserRateMultiplier, at)
	return frozen, err
}

func (r fixedKeywordPolicyGroupResolver) ResolveBlockedKeywordRequestGroup(context.Context, *service.APIKey, string, string, []byte, bool) (*service.KeywordRouteResult, error) {
	return &service.KeywordRouteResult{Group: r.group, Subscription: r.subscription}, nil
}

func TestRunContentModerationImageRerouteReplacesRequestGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	originalID, effectiveID := int64(10), int64(20)
	original := &service.Group{ID: originalID, Name: "strict", Platform: service.PlatformOpenAI, Status: service.StatusActive}
	effective := &service.Group{
		ID: effectiveID, Name: "image-keyword-allowed", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		AllowBlockedKeywordRequests: true, AllowImageGeneration: true, ImageRateIndependent: true,
	}
	subscription := &service.UserSubscription{ID: 99, GroupID: effectiveID}
	apiKey := &service.APIKey{ID: 8, GroupID: &originalID, User: &service.User{ID: 7}, Group: original}

	decision := runContentModeration(
		c, nil, svc, fixedKeywordPolicyGroupResolver{group: effective, subscription: subscription}, apiKey,
		middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIImages, "gpt-image-2",
		[]byte(`{"model":"gpt-image-2","prompt":"forbidden image request"}`),
	)
	require.NotNil(t, decision)
	require.True(t, decision.Allowed)
	require.True(t, decision.Rerouted)
	require.NotNil(t, apiKey.SchedulerGroupID())
	require.Equal(t, effectiveID, *apiKey.SchedulerGroupID())
	require.Same(t, effective, apiKey.Group)
	require.True(t, service.GroupAllowsImageGeneration(apiKey.Group))
	requestGroup, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group)
	require.True(t, ok)
	require.Same(t, effective, requestGroup)
	actualSubscription, ok := middleware.GetSubscriptionFromContext(c)
	require.True(t, ok)
	require.Same(t, subscription, actualSubscription)
	require.Equal(t, "10", c.Writer.Header().Get("X-Gateway-Original-Group-ID"))
	require.Equal(t, "20", c.Writer.Header().Get("X-Gateway-Effective-Group-ID"))
	require.Equal(t, "rerouted", c.Writer.Header().Get("X-Keyword-Policy-Route"))
}

func TestRunContentModerationRerouteKeepsOriginalRequestBillingTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, SampleRate: 100,
		AllGroups: true, BlockMessage: "blocked", BlockedKeywords: []string{"forbidden"},
		KeywordBlockingMode: service.ContentModerationKeywordModeKeywordOnly,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(
		&contentModerationHandlerSettingRepo{values: map[string]string{
			service.SettingKeyRiskControlEnabled:      "true",
			service.SettingKeyContentModerationConfig: string(rawCfg),
		}},
		&contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil,
	)

	startedAt := time.Date(2026, time.August, 1, 23, 30, 0, 0, time.UTC)
	originalID, effectiveID := int64(10), int64(20)
	original := &service.Group{ID: originalID, Name: "strict", Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1}
	effective := &service.Group{
		ID: effectiveID, Name: "allowed", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		RateMultiplier: 1.5, AllowBlockedKeywordRequests: true,
		PeakRateEnabled: true, PeakStart: "22:00", PeakEnd: "06:00", PeakRateMultiplier: 2, PeakTimezone: "UTC",
	}
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	const prefix = "keyword-rate-resize"
	quota := gatewaylite.NewRedisQuota(client, prefix)
	oldQuota := gatewaylite.DefaultRedisQuota()
	oldRuntime := gatewaylite.DefaultRuntimeConfigRef()
	gatewaylite.SetDefaultRedisQuota(quota)
	gatewaylite.SetDefaultRuntimeConfigRef(gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{RedisPrefix: prefix}))
	defer func() {
		gatewaylite.SetDefaultRedisQuota(oldQuota)
		gatewaylite.SetDefaultRuntimeConfigRef(oldRuntime)
	}()
	require.NoError(t, quota.EnsureLease(context.Background(), gatewaylite.LeaseSnapshot{
		LeaseID: "lease-keyword-rate", UserID: 7, Region: "us", AllocatedCents: 20,
		Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}))
	_, _, reserved, err := quota.ReserveActiveLease(context.Background(), gatewaylite.ReserveRequest{
		RequestID: "request-keyword-rate", KeyID: "key-keyword-rate", UserID: 7, Region: "us",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	reservation := gatewaylite.NewRequestReservation(1, "lease-keyword-rate")
	baseCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "request-keyword-rate", KeyID: "key-keyword-rate", UserID: 7,
		LeaseID: "lease-keyword-rate", Region: "us", GatewayID: "us-1", Reservation: reservation,
	})
	requestCtx, _, err := service.FreezeEffectiveRatePolicy(baseCtx, original, nil, startedAt)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(requestCtx)
	apiKey := &service.APIKey{ID: 8, GroupID: &originalID, User: &service.User{ID: 7}, Group: original}
	resolver := &rateFreezingKeywordPolicyGroupResolver{group: effective}

	decision := runContentModeration(
		c, nil, svc, resolver, apiKey, middleware.AuthSubject{UserID: 7},
		service.ContentModerationProtocolOpenAIResponses, "gpt-test",
		[]byte(`{"input":"forbidden request"}`),
	)

	require.NotNil(t, decision)
	require.True(t, decision.Rerouted)
	require.Equal(t, startedAt.UnixMilli(), resolver.frozenAt.UnixMilli())
	policy, ok := service.EffectiveRatePolicyFromContext(c.Request.Context())
	require.True(t, ok)
	require.Equal(t, startedAt.UnixMilli(), policy.ResolvedAtMillis)
	require.InDelta(t, 3.0, policy.EffectiveRateMultiplier, 1e-12)
	estimatedCents, _, forceRefund := reservation.Snapshot()
	require.EqualValues(t, 3, estimatedCents)
	require.False(t, forceRefund)
	requestFields, err := client.HGetAll(context.Background(), prefix+":req:request-keyword-rate").Result()
	require.NoError(t, err)
	require.Equal(t, "3", requestFields["estimated_cents"])
}
