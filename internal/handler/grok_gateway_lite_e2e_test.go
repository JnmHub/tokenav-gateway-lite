package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type grokGatewayLiteE2EControl struct {
	key             gatewaylite.KeySnapshot
	lease           gatewaylite.LeaseSnapshot
	mu              sync.Mutex
	resolveRequests []gatewaylite.ResolveKeyRequest
	events          []gatewaylite.UsageEvent
	acquireCount    atomic.Int32
	rebalanceCount  atomic.Int32
}

func (c *grokGatewayLiteE2EControl) ResolveKey(_ context.Context, req gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	c.mu.Lock()
	c.resolveRequests = append(c.resolveRequests, req)
	c.mu.Unlock()
	return &gatewaylite.ResolveKeyResponse{OK: true, Key: c.key}, nil
}

func (c *grokGatewayLiteE2EControl) AcquireLease(_ context.Context, _ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	c.acquireCount.Add(1)
	return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: c.lease}, nil
}

func (c *grokGatewayLiteE2EControl) RebalanceLease(_ context.Context, _ gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	c.rebalanceCount.Add(1)
	return &gatewaylite.RebalanceLeaseResponse{OK: false, Error: "unexpected_rebalance"}, nil
}

func (c *grokGatewayLiteE2EControl) ReportUsage(_ context.Context, event gatewaylite.UsageEvent) error {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	return nil
}

func (c *grokGatewayLiteE2EControl) snapshot() ([]gatewaylite.ResolveKeyRequest, []gatewaylite.UsageEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]gatewaylite.ResolveKeyRequest(nil), c.resolveRequests...), append([]gatewaylite.UsageEvent(nil), c.events...)
}

type grokGatewayLiteE2EAccountRepo struct {
	service.AccountRepository
	account service.Account
	mu      sync.Mutex
	groups  []int64
}

func (r *grokGatewayLiteE2EAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	r.mu.Lock()
	r.groups = append(r.groups, groupID)
	r.mu.Unlock()
	if groupID != r.account.GroupIDs[0] || platform != r.account.Platform {
		return nil, nil
	}
	return []service.Account{r.account}, nil
}

func (r *grokGatewayLiteE2EAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	if platform != r.account.Platform {
		return nil, nil
	}
	return []service.Account{r.account}, nil
}

func (r *grokGatewayLiteE2EAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *grokGatewayLiteE2EAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.account.ID {
		return nil, service.ErrNoAvailableAccounts
	}
	account := r.account
	return &account, nil
}

func (r *grokGatewayLiteE2EAccountRepo) selectedGroups() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.groups...)
}

type grokGatewayLiteE2EUsageLogRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []*service.UsageLog
}

func (r *grokGatewayLiteE2EUsageLogRepo) Create(_ context.Context, usageLog *service.UsageLog) (bool, error) {
	copyLog := *usageLog
	r.mu.Lock()
	r.logs = append(r.logs, &copyLog)
	r.mu.Unlock()
	return true, nil
}

func (r *grokGatewayLiteE2EUsageLogRepo) ResolveGatewayLiteUsageIdentity(_ context.Context, remoteUserID int64, remoteKeyID string) (int64, int64, error) {
	if remoteKeyID == "grok-e2e-key" {
		return remoteUserID, 7001, nil
	}
	return 0, 0, nil
}

func (r *grokGatewayLiteE2EUsageLogRepo) snapshot() []*service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...)
}

type grokGatewayLiteE2EBillingRepo struct {
	mu       sync.Mutex
	commands []*service.UsageBillingCommand
}

func (r *grokGatewayLiteE2EBillingRepo) Apply(_ context.Context, command *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	copyCommand := *command
	r.mu.Lock()
	r.commands = append(r.commands, &copyCommand)
	r.mu.Unlock()
	// gateway-lite（轻量网关）的真实扣费权威在主站；本地仓库只用于证明
	// 同一个规范 request_id 已经贯穿到计费命令，不在测试里模拟第二次扣款。
	return &service.UsageBillingApplyResult{Applied: false}, nil
}

func (r *grokGatewayLiteE2EBillingRepo) snapshot() []*service.UsageBillingCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageBillingCommand(nil), r.commands...)
}

type grokGatewayLiteE2EVideoStore struct {
	mu              sync.Mutex
	tasks           map[string]*service.GrokVideoTask
	pollLocks       map[string]bool
	settlementLocks map[string]bool
}

func newGrokGatewayLiteE2EVideoStore() *grokGatewayLiteE2EVideoStore {
	return &grokGatewayLiteE2EVideoStore{
		tasks:           make(map[string]*service.GrokVideoTask),
		pollLocks:       make(map[string]bool),
		settlementLocks: make(map[string]bool),
	}
}

func (s *grokGatewayLiteE2EVideoStore) Ready(context.Context) error { return nil }

func (s *grokGatewayLiteE2EVideoStore) Save(_ context.Context, task *service.GrokVideoTask) error {
	copyTask := *task
	s.mu.Lock()
	s.tasks[task.RequestID] = &copyTask
	s.mu.Unlock()
	return nil
}

func (s *grokGatewayLiteE2EVideoStore) Get(_ context.Context, requestID string) (*service.GrokVideoTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[requestID]
	if task == nil {
		return nil, service.ErrGrokVideoTaskNotFound
	}
	copyTask := *task
	return &copyTask, nil
}

func (s *grokGatewayLiteE2EVideoStore) ListDue(context.Context, time.Time, int) ([]*service.GrokVideoTask, error) {
	return nil, nil
}

func (s *grokGatewayLiteE2EVideoStore) TryLockPolling(_ context.Context, requestID string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pollLocks[requestID] {
		return false, nil
	}
	s.pollLocks[requestID] = true
	return true, nil
}

func (s *grokGatewayLiteE2EVideoStore) UnlockPolling(_ context.Context, requestID string) error {
	s.mu.Lock()
	delete(s.pollLocks, requestID)
	s.mu.Unlock()
	return nil
}

func (s *grokGatewayLiteE2EVideoStore) TryLockSettlement(_ context.Context, requestID string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settlementLocks[requestID] {
		return false, nil
	}
	s.settlementLocks[requestID] = true
	return true, nil
}

func (s *grokGatewayLiteE2EVideoStore) UnlockSettlement(_ context.Context, requestID string) error {
	s.mu.Lock()
	delete(s.settlementLocks, requestID)
	s.mu.Unlock()
	return nil
}

func (s *grokGatewayLiteE2EVideoStore) CleanupFinalized(context.Context, time.Time) (int64, error) {
	return 0, nil
}

type grokGatewayLiteE2EUpstreamCall struct {
	Method    string
	Path      string
	Body      string
	AccountID int64
}

type grokGatewayLiteE2EHTTPUpstream struct {
	client *http.Client
	mu     sync.Mutex
	calls  []grokGatewayLiteE2EUpstreamCall
}

func (u *grokGatewayLiteE2EHTTPUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	u.mu.Lock()
	u.calls = append(u.calls, grokGatewayLiteE2EUpstreamCall{
		Method:    req.Method,
		Path:      req.URL.Path,
		Body:      string(body),
		AccountID: accountID,
	})
	u.mu.Unlock()
	return u.client.Do(req)
}

func (u *grokGatewayLiteE2EHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *grokGatewayLiteE2EHTTPUpstream) snapshot() []grokGatewayLiteE2EUpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]grokGatewayLiteE2EUpstreamCall(nil), u.calls...)
}

func TestGrokGatewayLiteFullChainWithFixedGroupAndCanonicalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Xai-Request-Id", "xai-text-e2e")
			_, _ = w.Write([]byte(`{"id":"resp-grok-e2e","object":"response","model":"grok-4.5","output":[],"usage":{"input_tokens":1000000,"output_tokens":500000,"input_tokens_details":{"cached_tokens":200000}}}`))
		case "/v1/images/generations":
			w.Header().Set("Xai-Request-Id", "xai-image-e2e")
			_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.example/image-1.png"},{"url":"https://cdn.example/image-2.png"}]}`))
		case "/v1/videos/generations":
			w.Header().Set("Xai-Request-Id", "xai-video-generation-e2e")
			_, _ = w.Write([]byte(`{"request_id":"video-e2e-1"}`))
		case "/v1/videos/video-e2e-1":
			w.Header().Set("Xai-Request-Id", "xai-video-status-e2e")
			_, _ = w.Write([]byte(`{"id":"video-e2e-1","status":"completed","url":"https://cdn.example/video.mp4","resolution":"720p","duration":10}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	prefix := "grok-full-chain"
	quota := gatewaylite.NewRedisQuota(redisClient, prefix)
	previousQuota := gatewaylite.DefaultRedisQuota()
	gatewaylite.SetDefaultRedisQuota(quota)
	t.Cleanup(func() {
		if previousQuota != nil {
			gatewaylite.SetDefaultRedisQuota(previousQuota)
			return
		}
		gatewaylite.SetDefaultRedisQuota(gatewaylite.NewRedisQuota(nil, ""))
	})

	const (
		globalGroupID = int64(3000000009)
		localGroupID  = int64(9)
		userID        = int64(42)
		accountID     = int64(451)
	)
	secret := "grok-e2e-secret"
	secretHash := sha256.Sum256([]byte(secret))
	apiKeyValue := "aiceo_sk_grok-e2e-key_" + secret
	control := &grokGatewayLiteE2EControl{
		key: gatewaylite.KeySnapshot{
			KeyID:                   "grok-e2e-key",
			UserID:                  userID,
			SecretHash:              hex.EncodeToString(secretHash[:]),
			Status:                  service.StatusActive,
			Platform:                service.PlatformGrok,
			GroupID:                 globalGroupID,
			GroupName:               "Grok 完整链路",
			AllowedModels:           []string{"grok-4.5", "grok-imagine-image", "grok-imagine-video-1.5"},
			Concurrency:             4,
			GatewayAccessEnforced:   true,
			GatewayStrategy:         "fixed_group",
			KeyDefaultGatewayNodeID: 116,
			AvailableGateways: []gatewaylite.GatewayRouteSummary{{
				ID:     116,
				Code:   "gw-e2e",
				Region: "test-region",
			}},
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID:                    globalGroupID,
				LocalGroupID:          localGroupID,
				Name:                  "Grok 完整链路",
				Platform:              service.PlatformGrok,
				Status:                service.StatusActive,
				RateMultiplier:        1.5,
				AllowImageGeneration:  true,
				AllowMessagesDispatch: true,
			},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease-grok-e2e",
			UserID:         userID,
			Region:         "gw-e2e",
			AllocatedCents: 10000,
			ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		},
	}
	reporter := gatewaylite.NewRedisUsageReporter(redisClient, prefix, control)
	workerCtx, stopWorker := context.WithCancel(context.Background())
	reporter.StartWorkers(workerCtx, 1)
	t.Cleanup(stopWorker)
	previousReporter := gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previousReporter) })

	accountRate := 1.0
	accountRepo := &grokGatewayLiteE2EAccountRepo{account: service.Account{
		ID:             accountID,
		Name:           "grok-e2e-account",
		Platform:       service.PlatformGrok,
		Type:           service.AccountTypeAPIKey,
		Status:         service.StatusActive,
		Schedulable:    true,
		Concurrency:    4,
		RateMultiplier: &accountRate,
		GroupIDs:       []int64{localGroupID},
		Credentials: map[string]any{
			"api_key":  "xai-e2e-key",
			"base_url": upstreamServer.URL + "/v1",
			"model_mapping": map[string]any{
				"grok-4.5":               "grok-4.5",
				"grok-imagine-image":     "grok-imagine-image",
				"grok-imagine-video":     "grok-imagine-video",
				"grok-imagine-video-1.5": "grok-imagine-video-1.5",
			},
		},
	}}
	usageRepo := &grokGatewayLiteE2EUsageLogRepo{}
	billingRepo := &grokGatewayLiteE2EBillingRepo{}
	videoStore := newGrokGatewayLiteE2EVideoStore()
	upstream := &grokGatewayLiteE2EHTTPUpstream{client: upstreamServer.Client()}

	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.MaxAccountSwitches = 3
	billingService := service.NewBillingService(cfg, nil)
	billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	concurrencyCache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	concurrencyService := service.NewConcurrencyService(concurrencyCache)
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		billingRepo,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		concurrencyService,
		billingService,
		nil,
		billingCacheService,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	gatewayService.SetGrokVideoTaskStore(videoStore)
	apiKeyService := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
	h := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCacheService,
		apiKeyService,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	router := gin.New()
	router.Use(middleware2.ClientRequestID())
	router.Use(InboundEndpointMiddleware())
	router.Use(gin.HandlerFunc(middleware2.NewGatewayLiteAPIKeyAuthMiddleware(control, "test-region", "gw-e2e", quota, nil, reporter)))
	router.POST("/v1/responses", h.Responses)
	router.POST("/v1/images/generations", h.GrokImages)
	router.POST("/v1/videos/generations", h.GrokVideoGeneration)
	router.GET("/v1/videos/:request_id", h.GrokVideoStatus)

	perform := func(method, path, requestID, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxkey.ClientRequestID, requestID))
		req.Header.Set("Authorization", "Bearer "+apiKeyValue)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	textResponse := perform(http.MethodPost, "/v1/responses", "grok-text-e2e", `{"model":"grok-4.5","input":"hello","stream":false}`)
	require.Equal(t, http.StatusOK, textResponse.Code, textResponse.Body.String())
	require.Contains(t, textResponse.Body.String(), "resp-grok-e2e")

	imageResponse := perform(http.MethodPost, "/v1/images/generations", "grok-image-e2e", `{"model":"grok-imagine-image","prompt":"draw","size":"1024x1024"}`)
	require.Equal(t, http.StatusOK, imageResponse.Code, imageResponse.Body.String())
	require.Contains(t, imageResponse.Body.String(), "image-2.png")

	videoGenerationResponse := perform(http.MethodPost, "/v1/videos/generations", "grok-video-e2e", `{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"720p","duration":10}`)
	require.Equal(t, http.StatusOK, videoGenerationResponse.Code, videoGenerationResponse.Body.String())
	require.Contains(t, videoGenerationResponse.Body.String(), "video-e2e-1")

	videoStatusResponse := perform(http.MethodGet, "/v1/videos/video-e2e-1", "grok-video-status-e2e", "")
	require.Equal(t, http.StatusOK, videoStatusResponse.Code, videoStatusResponse.Body.String())
	require.Contains(t, videoStatusResponse.Body.String(), "completed")

	require.Eventually(t, func() bool {
		_, events := control.snapshot()
		return len(events) >= 6 && len(usageRepo.snapshot()) == 3 && len(billingRepo.snapshot()) == 3
	}, 5*time.Second, 20*time.Millisecond)

	resolveRequests, events := control.snapshot()
	require.Len(t, resolveRequests, 4)
	require.EqualValues(t, 1, control.acquireCount.Load(), "the Redis lease should be fetched once and reused on the hot path")
	require.Zero(t, control.rebalanceCount.Load())
	var videoStatusResolveFound bool
	for _, request := range resolveRequests {
		if request.BillingExempt {
			videoStatusResolveFound = true
		}
	}
	require.True(t, videoStatusResolveFound)

	eventsByRequestID := make(map[string][]gatewaylite.UsageEvent)
	for _, event := range events {
		eventsByRequestID[event.RequestID] = append(eventsByRequestID[event.RequestID], event)
	}
	require.Len(t, eventsByRequestID["client:grok-text-e2e"], 2)
	require.Len(t, eventsByRequestID["client:grok-image-e2e"], 2)
	require.Len(t, eventsByRequestID["client:grok-video-e2e"], 2)
	require.NotContains(t, eventsByRequestID, "client:grok-video-status-e2e")

	assertUsageEvents := func(requestID string, actualCents int64) {
		t.Helper()
		for _, event := range eventsByRequestID[requestID] {
			require.Equal(t, userID, event.UserID)
			require.Equal(t, "grok-e2e-key", event.KeyID)
			require.Equal(t, localGroupID, event.GatewayGroupID)
			require.Equal(t, "Grok 完整链路", event.GatewayGroupName)
			require.InDelta(t, 1.5, event.RateMultiplier, 1e-9)
			require.Equal(t, actualCents, event.ActualCents)
		}
	}
	// 250K 输入命中 Grok 官方长上下文价梯，文本费用按 2 倍计算。
	assertUsageEvents("client:grok-text-e2e", 1398)
	assertUsageEvents("client:grok-image-e2e", 6)
	assertUsageEvents("client:grok-video-e2e", 75)

	var textDetail, imageDetail, videoDetail *gatewaylite.UsageEvent
	for i := range events {
		event := &events[i]
		switch {
		case event.RequestID == "client:grok-text-e2e" && event.TokensOut > 0:
			textDetail = event
		case event.RequestID == "client:grok-image-e2e" && event.ImageCount > 0:
			imageDetail = event
		case event.RequestID == "client:grok-video-e2e" && event.VideoCount > 0:
			videoDetail = event
		}
	}
	require.NotNil(t, textDetail)
	require.EqualValues(t, 800000, textDetail.TokensIn)
	require.EqualValues(t, 500000, textDetail.TokensOut)
	require.EqualValues(t, 200000, textDetail.CacheReadTokens)
	require.Equal(t, string(service.BillingModeToken), textDetail.BillingMode)
	require.NotNil(t, imageDetail)
	require.EqualValues(t, 2, imageDetail.ImageCount)
	require.Equal(t, service.ImageBillingSize1K, imageDetail.ImageSize)
	require.Equal(t, string(service.BillingModeImage), imageDetail.BillingMode)
	require.NotNil(t, videoDetail)
	require.EqualValues(t, 1, videoDetail.VideoCount)
	require.Equal(t, service.VideoBillingResolution720P, videoDetail.VideoResolution)
	require.EqualValues(t, 10, videoDetail.VideoDurationSeconds)
	require.Equal(t, string(service.BillingModeVideo), videoDetail.BillingMode)

	usageLogs := usageRepo.snapshot()
	logsByRequestID := make(map[string]*service.UsageLog, len(usageLogs))
	for _, usageLog := range usageLogs {
		logsByRequestID[usageLog.RequestID] = usageLog
		require.EqualValues(t, 7001, usageLog.APIKeyID)
		require.NotNil(t, usageLog.GroupID)
		require.Equal(t, localGroupID, *usageLog.GroupID)
		require.InDelta(t, 1.5, usageLog.RateMultiplier, 1e-9)
	}
	require.InDelta(t, 13.98, logsByRequestID["client:grok-text-e2e"].ActualCost, 1e-9)
	require.InDelta(t, 0.06, logsByRequestID["client:grok-image-e2e"].ActualCost, 1e-9)
	require.InDelta(t, 0.75, logsByRequestID["client:grok-video-e2e"].ActualCost, 1e-9)

	billingCommands := billingRepo.snapshot()
	commandIDs := make(map[string]bool, len(billingCommands))
	for _, command := range billingCommands {
		commandIDs[command.RequestID] = true
	}
	require.True(t, commandIDs["client:grok-text-e2e"])
	require.True(t, commandIDs["client:grok-image-e2e"])
	require.True(t, commandIDs["client:grok-video-e2e"])
	require.False(t, commandIDs["client:grok-video-status-e2e"])

	for _, groupID := range accountRepo.selectedGroups() {
		require.Equal(t, localGroupID, groupID)
	}
	upstreamCalls := upstream.snapshot()
	require.Len(t, upstreamCalls, 4)
	for _, call := range upstreamCalls {
		require.Equal(t, accountID, call.AccountID)
	}
	require.Equal(t, "/v1/responses", upstreamCalls[0].Path)
	require.Contains(t, upstreamCalls[0].Body, `"model":"grok-4.5"`)
	require.Equal(t, "/v1/images/generations", upstreamCalls[1].Path)
	require.Contains(t, upstreamCalls[1].Body, `"model":"grok-imagine-image"`)
	require.Equal(t, "/v1/videos/generations", upstreamCalls[2].Path)
	require.Contains(t, upstreamCalls[2].Body, `"model":"grok-imagine-video"`)
	require.Equal(t, "/v1/videos/video-e2e-1", upstreamCalls[3].Path)
	require.Equal(t, http.MethodGet, upstreamCalls[3].Method)

	task, err := videoStore.Get(context.Background(), "video-e2e-1")
	require.NoError(t, err)
	require.Equal(t, service.GrokVideoTaskStateBilled, task.State)
	require.Equal(t, "client:grok-video-e2e", task.QuotaRequestID)
	require.EqualValues(t, 75, task.ReservedCents)
	require.NotNil(t, task.BillingSnapshot)
	require.InDelta(t, 1.5, task.BillingSnapshot.RateMultiplier, 1e-9)

	leaseFields, err := redisClient.HGetAll(context.Background(), prefix+":lease:42:gw-e2e").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, gatewaylite.ParseInt64Field(leaseFields, "reserved_cents"))
	require.EqualValues(t, 1479, gatewaylite.ParseInt64Field(leaseFields, "spent_cents"))
	statusReservationExists, err := redisClient.Exists(context.Background(), prefix+":req:client:grok-video-status-e2e").Result()
	require.NoError(t, err)
	require.Zero(t, statusReservationExists, "video status query must not create a separate quota reservation")
}
