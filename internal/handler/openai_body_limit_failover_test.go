//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIBodyLimitFailoverHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	bodies     [][]byte
}

func (u *openAIBodyLimitFailoverHTTPUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()

	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.bodies = append(u.bodies, append([]byte(nil), body...))
	u.mu.Unlock()

	if accountID == 7101 {
		return &http.Response{
			StatusCode: http.StatusRequestEntityTooLarge,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"rid-body-limit-first"},
			},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"request body exceeds this account's 16MB proxy limit; secret=must-not-leak"}}`)),
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"rid-body-limit-success"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_body_limit_success","object":"response","status":"completed","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)),
	}, nil
}

func (u *openAIBodyLimitFailoverHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *openAIBodyLimitFailoverHTTPUpstream) snapshot() ([]int64, [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	ids := append([]int64(nil), u.accountIDs...)
	bodies := make([][]byte, len(u.bodies))
	for i := range u.bodies {
		bodies[i] = append([]byte(nil), u.bodies[i]...)
	}
	return ids, bodies
}

type openAIBodyLimitUsageRepo struct {
	service.UsageLogRepository
	mu      sync.Mutex
	logs    []*service.UsageLog
	metas   []gatewaylite.RequestMeta
	metaOKs []bool
}

func (r *openAIBodyLimitUsageRepo) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	meta, ok := gatewaylite.RequestMetaFromContext(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	r.metas = append(r.metas, meta)
	r.metaOKs = append(r.metaOKs, ok)
	return true, nil
}

func (r *openAIBodyLimitUsageRepo) snapshot() ([]*service.UsageLog, []gatewaylite.RequestMeta, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...), append([]gatewaylite.RequestMeta(nil), r.metas...), append([]bool(nil), r.metaOKs...)
}

func TestOpenAIBodyLimitFailover_FixedGroupSwitchesOnceAndRecordsSingleUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7100)
	accounts := []service.Account{
		{
			ID: 7101, Name: "body-limit-small", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": "sk-first"},
			Extra:       map[string]any{"openai_responses_supported": true},
		},
		{
			ID: 7102, Name: "body-limit-large", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": "sk-second"},
			Extra:       map[string]any{"openai_responses_supported": true},
		},
	}

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxAccountSwitches = 3
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	usageRepo := &openAIBodyLimitUsageRepo{}
	upstream := &openAIBodyLimitFailoverHTTPUpstream{}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		upstream,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	handler := NewOpenAIGatewayHandler(
		gatewaySvc,
		service.NewConcurrencyService(cache),
		billingCacheSvc,
		&service.APIKeyService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	user := &service.User{ID: 7201, Status: service.StatusActive, Balance: 100, Concurrency: 1}
	group := &service.Group{ID: groupID, Name: "fixed-openai", Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true}
	apiKey := &service.APIKey{
		ID: 7301, UserID: user.ID, GroupID: &groupID, Group: group, User: user, Status: service.StatusActive,
		GatewayStrategy: "fixed_group", KeyDefaultGatewayNodeID: 1,
	}
	requestBody := `{"model":"gpt-5.6-sol","stream":false,"input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(gatewaylite.ContextWithRequestMeta(req.Context(), gatewaylite.RequestMeta{
		RequestID: "request-body-limit-1",
		KeyID:     "key-body-limit",
		UserID:    user.ID,
		LeaseID:   "lease-body-limit-1",
		Region:    "test",
	}))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})

	handler.Responses(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "resp_body_limit_success")
	accountIDs, bodies := upstream.snapshot()
	require.Equal(t, []int64{7101, 7102}, accountIDs)
	require.Len(t, bodies, 2)
	require.Equal(t, bodies[0], bodies[1], "the immutable RequestBodyRef must replay the same normalized JSON for each account")

	logs, metas, metaOKs := usageRepo.snapshot()
	require.Len(t, logs, 1, "the failed 413 attempt must not create usage or billing facts")
	require.Equal(t, int64(7102), logs[0].AccountID)
	require.Equal(t, "request-body-limit-1", logs[0].RequestID)
	require.Equal(t, []bool{true}, metaOKs)
	require.Len(t, metas, 1)
	require.Equal(t, "lease-body-limit-1", metas[0].LeaseID, "the final usage fact must retain the request's single quota lease")
}

func TestOpenAIBodyLimitFailoverExhausted_ReturnsRedactedJSON413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, bodyLimitFailoverTestError(), false)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	errBody, ok := envelope["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "invalid_request_error", errBody["type"])
	require.Equal(t, "invalid_request", errBody["code"])
	require.Equal(t, service.OpenAIRequestBodyTooLargeClientMessage, errBody["message"])
	require.NotContains(t, rec.Body.String(), "must-not-leak")
}

func TestOpenAIBodyLimitFailoverExhausted_ReturnsRedactedResponsesSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, bodyLimitFailoverTestError(), true)

	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, "event: response.failed\n"), body)
	require.Contains(t, body, `"code":"invalid_request"`)
	require.Contains(t, body, `"message":"Request payload is too large"`)
	require.NotContains(t, body, "must-not-leak")
}

func bodyLimitFailoverTestError() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:        http.StatusRequestEntityTooLarge,
		ResponseBody:      []byte(`{"error":{"message":"proxy limit secret=must-not-leak"}}`),
		Scope:             service.GatewayFailureScopeAccount,
		Reason:            service.GatewayFailureReason("openai_request_body_too_large"),
		NextAccountAction: service.NextAccountRetry,
		ClientStatusCode:  http.StatusRequestEntityTooLarge,
		ClientMessage:     service.OpenAIRequestBodyTooLargeClientMessage,
	}
}

var _ service.HTTPUpstream = (*openAIBodyLimitFailoverHTTPUpstream)(nil)
