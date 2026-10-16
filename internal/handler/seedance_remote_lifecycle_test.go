package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSeedanceRemoteLifecycleReliableCompletion(t *testing.T) {
	runSeedanceRemoteLifecycleReliableCompletion(t, "gpt-5.5", "ep-video", "gpt-5.5", false)
}

func TestSeedanceMappedPricingLifecycle(t *testing.T) {
	for _, test := range []struct {
		name, requested, upstream, billed string
		compositeWithoutFunding           bool
	}{
		{"mapped_only_has_price", "customer-video-alias-no-catalog", "gpt-5.5", "gpt-5.5", false},
		{"customer_price_stays_first", "gpt-5.5", "gpt-5.5-pro", "gpt-5.5", false},
		{"composite_without_funding_mapped_price_stays_frozen", "customer-video-alias-no-catalog", "gpt-5.5", "gpt-5.5", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runSeedanceRemoteLifecycleReliableCompletion(t, test.requested, test.upstream, test.billed, test.compositeWithoutFunding)
		})
	}
}

func runSeedanceRemoteLifecycleReliableCompletion(t *testing.T, requestedModel, upstreamModel, billingModel string, compositeWithoutFunding bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	control := &grokGatewayLiteE2EControl{}
	reporter := gatewaylite.NewRedisUsageReporter(client, "seedance-e2e", control)
	previous := gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })
	workerCtx, stop := context.WithCancel(context.Background())
	reporter.StartWorkers(workerCtx, 1)
	t.Cleanup(stop)

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var sent map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
			require.Equal(t, upstreamModel, sent["model"], "原账号映射目标必须真实发送")
			_, _ = w.Write([]byte(`{"id":"remote-task","status":"queued"}`))
		} else if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		} else {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"id": "remote-task", "status": "succeeded", "model": upstreamModel, "usage": map[string]any{"completion_tokens": 1000000}}))
		}
	}))
	defer upstreamServer.Close()
	groupID := int64(7)
	account := service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Schedulable: true, Concurrency: 5, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "test", "base_url": upstreamServer.URL + "/api/v3", "openai_capabilities": []string{"seedance"}, "model_mapping": map[string]any{requestedModel: upstreamModel}}}
	accountRepo := &grokGatewayLiteE2EAccountRepo{account: account}
	usageRepo := &grokGatewayLiteE2EUsageLogRepo{}
	billingRepo := &grokGatewayLiteE2EBillingRepo{}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Default.RateMultiplier = 1
	// 该用例使用内置 GPT-5.5 token 价格作为客户配置模型，验证原任务真实 completion_tokens。
	billing := service.NewBillingService(cfg, nil)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(&concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }})
	upstream := &grokGatewayLiteE2EHTTPUpstream{client: upstreamServer.Client()}
	gateway := service.NewOpenAIGatewayService(accountRepo, usageRepo, billingRepo, nil, nil, nil, repository.NewGatewayCache(client), cfg, nil, concurrency, billing, nil, billingCache, upstream, nil, nil, nil, nil, nil, nil, nil)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billingCache, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, nil, cfg)
	key := &service.APIKey{ID: 0, UserID: 33, GroupID: &groupID, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 2, AllowImageGeneration: true}, User: &service.User{ID: 33}}
	auth := gatewaylite.FundingAuthorization{AuthorizationID: "seedance-original-auth", RequestID: "create-original", KeyID: "grok-e2e-key", UserID: 33,
		GatewayCode: "gateway-a", Region: "test", EntryGatewayNodeID: 9, EntryGroupID: groupID, RequestedModel: requestedModel, Endpoint: "/api/v3/contents/generations/tasks",
		BillingSubjectType: "user", BillingSubjectID: 33, Version: 3, Status: "active", Disposition: "authorized", UpperBoundMicroCents: 7000000000,
		ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli(), BoundEvidence: map[string]any{"kind": "fixed_media"},
		Items: []gatewaylite.FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "balance", ReservedMicroCents: 7000000000}}}
	quota := gatewaylite.NewRedisQuota(client, "seedance-e2e")
	meta := gatewaylite.RequestMeta{RequestID: auth.RequestID, KeyID: auth.KeyID, UserID: 33, GatewayID: "gateway-a", GroupID: groupID,
		BillingSubjectType: "user", BillingSubjectID: 33, Funding: gatewaylite.NewRequestFunding(auth, &seedanceSnapshotFundingControl{}, quota)}
	if compositeWithoutFunding {
		// 保留同一个混合分组 APIKey 和远程请求身份，验证旧无卡入口也只能使用创建冻结价。
		key.Group.Platform = service.PlatformComposite
		meta.Funding = nil
		// 普通余额完成必须提交创建时的真实预留；手工处理器夹具也补齐该生产前置。
		meta.LeaseID, meta.Region = "seedance-original-lease", "test"
		previousQuota := gatewaylite.DefaultRedisQuota()
		gatewaylite.SetDefaultRedisQuota(quota)
		t.Cleanup(func() { gatewaylite.SetDefaultRedisQuota(previousQuota) })
		require.NoError(t, quota.EnsureLease(context.Background(), gatewaylite.LeaseSnapshot{
			LeaseID: meta.LeaseID, UserID: meta.UserID, BillingSubjectType: meta.BillingSubjectType,
			BillingSubjectID: meta.BillingSubjectID, Region: meta.Region, AllocatedCents: 1000000,
			Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}))
		reserved, err := quota.Reserve(context.Background(), gatewaylite.ReserveRequest{
			RequestID: meta.RequestID, KeyID: meta.KeyID, UserID: meta.UserID,
			BillingSubjectType: meta.BillingSubjectType, BillingSubjectID: meta.BillingSubjectID,
			LeaseID: meta.LeaseID, Region: meta.Region, EstimatedCostCents: 2,
		})
		require.NoError(t, err)
		require.True(t, reserved)
	}
	policy, err := service.ResolveEffectiveRate(key.Group, nil, time.Now())
	require.NoError(t, err)
	createCtx := service.ContextWithEffectiveRatePolicy(gatewaylite.ContextWithRequestMeta(context.Background(), meta), policy)
	if compositeWithoutFunding {
		// Seedance 正式路由解析到 OpenAI；处理器用例也必须使用真实目标，不能用 Grok 绕过错误判断。
		createCtx = service.WithResolvedTargetPlatform(createCtx, service.PlatformOpenAI)
		require.False(t, gatewaylite.HasRequestFunding(createCtx))
	}
	makeContext := func(method string, ctx context.Context) (*gin.Context, *httptest.ResponseRecorder) {
		body := ""
		if method == http.MethodPost {
			payload, err := json.Marshal(map[string]any{"model": requestedModel, "content": []any{map[string]any{"type": "text", "text": "waves"}}})
			require.NoError(t, err)
			body = string(payload)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/api/v3/contents/generations/tasks/remote-task", strings.NewReader(body)).WithContext(ctx)
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "task_id", Value: "remote-task"}}
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 33, Concurrency: 5})
		return c, w
	}
	c, w := makeContext(http.MethodPost, createCtx)
	h.SeedanceTasks(c)
	require.Equal(t, 200, w.Code, w.Body.String())
	owner, err := service.ResolveMediaTaskIdentity(createCtx, key, 33)
	require.NoError(t, err)
	pending, err := gateway.LoadSeedancePendingBilling(createCtx, service.SeedanceTaskKey("remote-task"), owner)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, account.ID, pending.AccountID)
	if compositeWithoutFunding {
		require.Nil(t, pending.FundingSnapshot)
		require.NotNil(t, pending.RequestMeta)
		require.Equal(t, service.PlatformComposite, key.Group.Platform)
	} else {
		require.Equal(t, auth, *pending.FundingSnapshot)
	}
	require.Equal(t, requestedModel, pending.Model)
	require.Equal(t, upstreamModel, pending.UpstreamModel)
	require.Equal(t, billingModel, pending.BillingModel)
	require.Equal(t, billingModel, pending.ExecutionPricing.Model)
	require.NoError(t, service.ValidateSeedanceExecutionPricing(pending))
	// 模拟重启后的服务已改为完全不同的当前目录，完成只恢复原创建价。
	cfg.Pricing.DataDir = t.TempDir()
	changedPricing := service.NewPricingService(cfg, nil)
	for _, model := range []string{requestedModel, upstreamModel} {
		_, err = changedPricing.UpsertModelPricingOverride(model, service.LiteLLMModelPricing{InputCostPerToken: 1e-3, OutputCostPerToken: 2e-3})
		require.NoError(t, err)
	}
	changedBilling := service.NewBillingService(cfg, changedPricing)
	gateway = service.NewOpenAIGatewayService(accountRepo, usageRepo, billingRepo, nil, nil, nil, repository.NewGatewayCache(client), cfg, nil, concurrency, changedBilling, nil, billingCache, upstream, nil, nil, nil, nil, nil, nil, nil)
	h.gatewayService = gateway
	for i := 0; i < 3; i++ {
		pollMeta := meta
		pollMeta.RequestID, pollMeta.Funding = "free-poll", nil
		pollCtx := gatewaylite.ContextWithRequestMeta(context.Background(), pollMeta)
		c, w = makeContext(http.MethodGet, pollCtx)
		h.SeedanceTasks(c)
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	require.Eventually(t, func() bool { _, events := control.snapshot(); return len(events) == 1 }, 3*time.Second, 10*time.Millisecond)
	_, events := control.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, "create-original", events[0].RequestID)
	if compositeWithoutFunding {
		require.Empty(t, events[0].FundingAuthorizationID)
		require.Equal(t, meta.LeaseID, events[0].LeaseID)
	} else {
		require.Equal(t, auth.AuthorizationID, events[0].FundingAuthorizationID)
		require.Empty(t, events[0].LeaseID)
	}
	require.EqualValues(t, 1000000, events[0].TokensOut)
	require.InDelta(t, 2, events[0].RateMultiplier, 1e-12)
	require.EqualValues(t, 6000, events[0].ActualCents)
	require.Len(t, billingRepo.snapshot(), 1)
	require.Len(t, usageRepo.snapshot(), 1)
	assertOriginalQuota := func() {
		t.Helper()
		if !compositeWithoutFunding {
			return
		}
		lease, found, err := quota.LoadLeaseForSubject(context.Background(), gatewaylite.LeaseSubject{
			UserID: meta.UserID, BillingSubjectType: meta.BillingSubjectType, BillingSubjectID: meta.BillingSubjectID,
		}, meta.Region)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, meta.LeaseID, lease.LeaseID)
		require.EqualValues(t, 6000, lease.SpentCents)
		require.Zero(t, lease.ReservedCents)
		request, err := client.HGetAll(context.Background(), "seedance-e2e:req:"+meta.RequestID).Result()
		require.NoError(t, err)
		require.Equal(t, "committed", request["status"])
		require.Equal(t, "6000", request["actual_cents"])
		require.Equal(t, meta.LeaseID, request["lease_id"])
	}
	assertOriginalQuota()
	pollMeta := meta
	pollMeta.RequestID, pollMeta.Funding = "delete-free", nil
	c, w = makeContext(http.MethodDelete, gatewaylite.ContextWithRequestMeta(context.Background(), pollMeta))
	h.SeedanceTasks(c)
	require.Equal(t, http.StatusNoContent, w.Code)
	assertOriginalQuota()
	for _, call := range upstream.snapshot() {
		require.Equal(t, account.ID, call.AccountID)
	}
}

// unknown create 保留持久 admission；快照不可写必须在调用上游前拒绝。
func TestSeedanceCreateAdmissionAndUnknownResponse(t *testing.T) {
	cache := newSeedanceTestCache()
	groupID := int64(7)
	key := &service.APIKey{ID: 0, UserID: 33, GroupID: &groupID}
	meta := gatewaylite.RequestMeta{RequestID: "original-create", KeyID: "remote-key", UserID: 33, GatewayID: "gateway-a"}
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com/api/v3", "api_key": "test", "openai_capabilities": []string{"seedance"}}}
	h := newSeedanceOwnershipHandler(t, cache, account)
	prepared, err := h.gatewayService.PrepareSeedanceTaskCreation(ctx, key, 33, account, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	require.Len(t, cache.pending, 1)
	for _, raw := range cache.pending {
		var saved service.GrokVideoPendingBilling
		require.NoError(t, json.Unmarshal(raw, &saved))
		require.Equal(t, "original-create", saved.RequestMeta.RequestID)
		require.Equal(t, account.ID, saved.AccountID)
	}
	_, err = h.gatewayService.PrepareSeedanceTaskCreation(ctx, key, 33, account, "gpt-5.5", "gpt-5.5")
	require.ErrorIs(t, err, gatewaylite.ErrRequestQuotaUnavailable, "已有预登记不能重新提交上游")
	// 独立请求覆盖上游结果未知；使用可解析目录价卡，避免用无价格模型伪造执行快照。
	meta.RequestID = "unknown-create"
	ctx = gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	upstream := &seedanceUnknownUpstream{}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil)
	prepared, err = gateway.PrepareSeedanceTaskCreation(ctx, key, 33, account, "gpt-5.5", "gpt-5.5")
	require.NoError(t, err)
	c := seedanceTestRequest(t)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", nil).WithContext(prepared)
	_, err = gateway.ForwardSeedance(prepared, c, account, service.SeedanceEndpointCreate, "", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	unknown, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
	require.True(t, ok)
	require.True(t, unknown.AsyncPersistenceUncertain)
	require.True(t, unknown.AsyncSettlement)
	require.Equal(t, 1, upstream.calls, "模糊创建不得自动重试")
	require.Len(t, cache.pending, 2, "两个请求的预登记均保留原请求以便恢复")
	broken := newSeedanceTestGatewayService(nil, nil, &config.Config{})
	_, err = broken.PrepareSeedanceTaskCreation(ctx, key, 33, account, "video", "video")
	require.Error(t, err)
	// 身份0缺失 RequestMeta 不能降级为本地任务。
	_, err = service.ResolveMediaTaskIdentity(context.WithValue(context.Background(), ctxkey.RequestID, "local"), key, 33)
	require.Error(t, err)
}

type seedanceSnapshotFundingControl struct{}

func (*seedanceSnapshotFundingControl) AuthorizeFunding(context.Context, gatewaylite.AuthorizeFundingRequest) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, errors.New("test must reuse the already authorized funding")
}
func (*seedanceSnapshotFundingControl) ResizeFundingAuthorization(context.Context, gatewaylite.ResizeFundingAuthorizationRequest) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, errors.New("test must not resize the original funding")
}
func (*seedanceSnapshotFundingControl) ReleaseFundingAuthorization(context.Context, gatewaylite.ReleaseFundingAuthorizationRequest) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, errors.New("accepted task funding must stay reserved")
}
func (*seedanceSnapshotFundingControl) LookupFundingAuthorization(context.Context, string) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, errors.New("no funding recovery is needed in this test")
}

// 上游只提交一次，登记错误仅重试本地写入；提交结果不明时仍保留可追踪的 admission。
func TestSeedanceCreateRegistrationRecoveryAndUnknownWrite(t *testing.T) {
	for _, mode := range []string{"retry_final", "registration_unknown", "committed_unknown", "client_canceled"} {
		t.Run(mode, func(t *testing.T) {
			cache := &seedanceRegistrationCache{seedanceTestCache: newSeedanceTestCache()}
			cache.finalFailures = 1
			if mode == "registration_unknown" || mode == "committed_unknown" {
				cache.finalFailures = 2
			}
			cache.commitUnknown = mode == "committed_unknown"
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			meta := gatewaylite.RequestMeta{RequestID: "register-original", KeyID: "remote-key", UserID: 33, GatewayID: "gateway-a", LeaseID: "original-held-lease"}
			ctx := gatewaylite.ContextWithRequestMeta(base, meta)
			upstream := &seedanceAcceptedUpstream{}
			if mode == "client_canceled" {
				upstream.afterAccept = cancel
			}
			cfg := &config.Config{RunMode: config.RunModeGatewayLite}
			gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil)
			key := &service.APIKey{ID: 0, UserID: 33}
			account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://example.com/api/v3", "api_key": "test", "openai_capabilities": []string{"seedance"}}}
			prepared, err := gateway.PrepareSeedanceTaskCreation(ctx, key, 33, account, "gpt-5.5", "gpt-5.5")
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", nil).WithContext(prepared)
			result, err := gateway.ForwardSeedance(prepared, c, account, service.SeedanceEndpointCreate, "", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}]}`))
			require.Equal(t, 1, upstream.calls, "不得再次创建上游任务")
			require.Equal(t, 2, cache.finalAttempts, "重试边界仅是幂等任务登记")
			owner, identityErr := service.ResolveMediaTaskIdentity(ctx, key, 33)
			require.NoError(t, identityErr)
			admission, loadErr := gateway.LoadSeedancePendingBilling(context.Background(), "seedance-admission:register-original", owner)
			require.NoError(t, loadErr)
			require.Equal(t, service.SeedanceTaskKey("registered-task"), admission.TaskID)
			require.Equal(t, "register-original", admission.RequestMeta.RequestID)
			require.Equal(t, "original-held-lease", admission.RequestMeta.LeaseID)
			final, loadErr := gateway.LoadSeedancePendingBilling(context.Background(), service.SeedanceTaskKey("registered-task"), owner)
			require.NoError(t, loadErr)
			if mode == "registration_unknown" || mode == "committed_unknown" {
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, http.StatusServiceUnavailable, w.Code)
				require.Contains(t, w.Body.String(), "seedance_task_registration_uncertain")
				require.Contains(t, w.Body.String(), "registered-task")
				meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
				require.True(t, ok)
				require.True(t, meta.AsyncPersistenceUncertain)
				require.True(t, meta.AsyncSettlement)
				if mode == "committed_unknown" {
					require.NotNil(t, final, "写入成功但确认丢失也不得释放原占用或再次创建")
				} else {
					require.Nil(t, final)
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, w.Code)
				require.NotNil(t, final, "客户端取消也必须完成任务归属登记")
			}
		})
	}
}

type seedanceRegistrationCache struct {
	*seedanceTestCache
	finalFailures int
	finalAttempts int
	commitUnknown bool
}

func (c *seedanceRegistrationCache) SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.HasSuffix(key, ":seedance:registered-task") {
		c.finalAttempts++
		if c.finalFailures > 0 {
			c.finalFailures--
			if c.commitUnknown {
				if err := c.seedanceTestCache.SetGrokVideoPendingBilling(ctx, key, payload, ttl); err != nil {
					return err
				}
			}
			return context.DeadlineExceeded
		}
	}
	return c.seedanceTestCache.SetGrokVideoPendingBilling(ctx, key, payload, ttl)
}

type seedanceAcceptedUpstream struct {
	service.HTTPUpstream
	calls       int
	afterAccept func()
}

func (s *seedanceAcceptedUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	s.calls++
	if s.afterAccept != nil {
		s.afterAccept()
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"registered-task","status":"queued"}`))}, nil
}

type seedanceUnknownUpstream struct {
	service.HTTPUpstream
	calls int
}

func (s *seedanceUnknownUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	s.calls++
	return nil, context.DeadlineExceeded
}
