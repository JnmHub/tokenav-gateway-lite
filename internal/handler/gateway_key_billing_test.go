package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type keyBillingUserRateRepoStub struct {
	service.UserGroupRateRepository
	rates map[int64]float64
}

type keyBillingControlPlaneStub struct {
	key            gatewaylite.KeySnapshot
	resolveRequest gatewaylite.ResolveKeyRequest
	acquireCalls   int
	rebalanceCalls int
}

func (s *keyBillingControlPlaneStub) ResolveKey(_ context.Context, req gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	s.resolveRequest = req
	return &gatewaylite.ResolveKeyResponse{OK: true, Key: s.key}, nil
}

func (s *keyBillingControlPlaneStub) AcquireLease(context.Context, gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	s.acquireCalls++
	return &gatewaylite.AcquireLeaseResponse{OK: false}, nil
}

func (s *keyBillingControlPlaneStub) RebalanceLease(context.Context, gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	s.rebalanceCalls++
	return &gatewaylite.RebalanceLeaseResponse{OK: false}, nil
}

type keyBillingLocalGroupReader struct {
	group *service.Group
}

func (r keyBillingLocalGroupReader) GetGroup(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

func (s *keyBillingUserRateRepoStub) GetByUserID(_ context.Context, _ int64) (map[int64]float64, error) {
	return s.rates, nil
}

func TestGatewayKeyBillingInfoHandlerReturnsOnlyBillingFacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9)
	apiKey := &service.APIKey{
		ID:      11,
		UserID:  7,
		Name:    "must-not-leak-key-name",
		GroupID: &groupID,
		Group: &service.Group{
			ID:             groupID,
			Name:           "must-not-leak-group-name",
			RateMultiplier: 1.25,
		},
		User: &service.User{ID: 7, Email: "must-not-leak@example.com", Balance: 999},
	}

	recorder := serveKeyBillingHandler(t, apiKey, nil, &config.Config{RunMode: config.RunModeGatewayLite}, false)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "sub2api.key_billing", payload["object"])
	require.Equal(t, float64(1), payload["schema_version"])
	require.Equal(t, "token", payload["billing_scope"])
	require.Equal(t, 1.25, payload["group_rate_multiplier"])
	require.Equal(t, 1.25, payload["resolved_rate_multiplier"])
	require.Equal(t, 1.25, payload["effective_rate_multiplier"])
	require.Equal(t, false, payload["peak_rate_enabled"])
	require.NotContains(t, payload, "user_rate_multiplier")
	for _, forbidden := range []string{"id", "key", "name", "email", "balance", "group_id", "group_name", "price"} {
		require.NotContains(t, payload, forbidden)
	}
}

func TestGatewayKeyBillingInfoRouteUsesLocalPeakPolicyWithoutBillingSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "peak-secret"
	hash := sha256.Sum256([]byte(secret))
	now := time.Now().UTC()
	control := &keyBillingControlPlaneStub{key: gatewaylite.KeySnapshot{
		KeyID:                           "peak-key",
		UserID:                          42,
		SecretHash:                      hex.EncodeToString(hash[:]),
		Status:                          service.StatusActive,
		Tier:                            2,
		Platform:                        service.PlatformOpenAI,
		GroupID:                         7001,
		GroupName:                       "stale-main-site-group",
		AllowedModels:                   []string{"*"},
		Concurrency:                     1,
		GatewayStrategy:                 "fixed_group",
		KeyDefaultGatewayNodeID:         7,
		GatewayAccessEnforced:           true,
		AvailableGateways:               []gatewaylite.GatewayRouteSummary{{ID: 7, Code: "us-1"}},
		KeywordRouteGroupAccessEnforced: true,
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID: 7001, LocalGroupID: 17, Name: "stale-main-site-group",
			Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1,
		},
	}}
	localGroup := &service.Group{
		ID:                 17,
		Name:               "local-peak-group",
		Platform:           service.PlatformOpenAI,
		Status:             service.StatusActive,
		RateMultiplier:     1.25,
		PeakRateEnabled:    true,
		PeakStart:          now.Add(-2 * time.Hour).Format("15:04"),
		PeakEnd:            now.Add(2 * time.Hour).Format("15:04"),
		PeakRateMultiplier: 1.6,
		PeakTimezone:       "UTC",
		Hydrated:           true,
	}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "us-1", GatewayCode: "us-1"})
	router := gin.New()
	router.GET(
		"/v1/sub2api/billing",
		gin.HandlerFunc(middleware.NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(
			control, runtimeRef, nil, nil, nil, keyBillingLocalGroupReader{group: localGroup},
		)),
		GatewayKeyBillingInfoHandler(nil, &config.Config{RunMode: config.RunModeGatewayLite}),
	)

	request := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
	request.Header.Set("Authorization", "Bearer tokenav_sk_peak-key_"+secret)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.True(t, control.resolveRequest.BillingExempt)
	require.True(t, control.resolveRequest.SkipUsageTouch)
	require.Zero(t, control.acquireCalls)
	require.Zero(t, control.rebalanceCalls)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "sub2api.key_billing", payload["object"])
	require.Equal(t, 1.25, payload["group_rate_multiplier"])
	require.Equal(t, true, payload["peak_rate_enabled"])
	require.Equal(t, 1.6, payload["peak_rate_multiplier"])
	require.Equal(t, 1.6, payload["applied_peak_multiplier"])
	require.InDelta(t, 2.0, payload["effective_rate_multiplier"], 1e-12)
	require.NotEmpty(t, payload["policy_version"])
	require.NotContains(t, payload, "user_rate_multiplier")
}

func TestGatewayKeyBillingInfoHandlerUsesLocalUserOverride(t *testing.T) {
	groupID := int64(9)
	apiKey := &service.APIKey{
		ID: 11, UserID: 7, GroupID: &groupID,
		Group: &service.Group{ID: groupID, RateMultiplier: 1.25},
		User:  &service.User{ID: 7},
	}
	rateRepo := &keyBillingUserRateRepoStub{rates: map[int64]float64{groupID: 1.8}}
	apiKeyService := service.NewAPIKeyService(nil, nil, nil, nil, rateRepo, nil, &config.Config{})
	recorder := serveKeyBillingHandler(t, apiKey, apiKeyService, &config.Config{RunMode: config.RunModeGatewayLite}, true)
	require.Equal(t, http.StatusOK, recorder.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, 1.25, payload["group_rate_multiplier"])
	require.Equal(t, 1.8, payload["user_rate_multiplier"])
	require.Equal(t, 1.8, payload["resolved_rate_multiplier"])
	require.Equal(t, 1.8, payload["effective_rate_multiplier"])
}

func TestGatewayKeyBillingInfoHandlerRejectsUnsupportedOrInvalidState(t *testing.T) {
	groupID := int64(9)
	apiKey := &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, RateMultiplier: 1},
		User:    &service.User{ID: 7},
	}

	simple := serveKeyBillingHandler(t, apiKey, nil, &config.Config{RunMode: config.RunModeSimple}, false)
	require.Equal(t, http.StatusNotFound, simple.Code)

	apiKey.Group.RateMultiplier = math.NaN()
	invalid := serveKeyBillingHandler(t, apiKey, nil, &config.Config{RunMode: config.RunModeGatewayLite}, false)
	require.Equal(t, http.StatusInternalServerError, invalid.Code)
	require.Contains(t, invalid.Body.String(), "Billing information is unavailable")
}

func serveKeyBillingHandler(t *testing.T, apiKey *service.APIKey, apiKeyService *service.APIKeyService, cfg *config.Config, local bool) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
	if local {
		req = req.WithContext(service.ContextWithGatewayLiteLocalAPIKey(req.Context()))
	}
	c.Request = req
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	GatewayKeyBillingInfoHandler(apiKeyService, cfg)(c)
	return recorder
}
