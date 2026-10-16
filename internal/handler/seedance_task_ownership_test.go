package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// seedanceTestCache 只实现 Seedance 归属链路真正会用到的两个缓存面：
// 待结算快照与粘性会话绑定。其余方法由嵌入的接口占位（本测试不会调用）。
type seedanceTestCache struct {
	service.GatewayCache

	mu       sync.Mutex
	pending  map[string][]byte
	sessions map[string]int64
	claims   map[string]string
}

func newSeedanceTestCache() *seedanceTestCache {
	return &seedanceTestCache{pending: map[string][]byte{}, sessions: map[string]int64{}, claims: map[string]string{}}
}

func (c *seedanceTestCache) ClaimSeedanceBilling(_ context.Context, key, token string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claims[key] != "" {
		return false, nil
	}
	c.claims[key] = token
	return true, nil
}

func (c *seedanceTestCache) ConfirmSeedanceBilling(_ context.Context, key, token string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claims[key] != token {
		return false, nil
	}
	c.claims[key] = "done"
	return true, nil
}

func (c *seedanceTestCache) ReleaseSeedanceBilling(_ context.Context, key, token string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claims[key] != token {
		return false, nil
	}
	delete(c.claims, key)
	return true, nil
}

func (c *seedanceTestCache) GetGrokVideoPendingBilling(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	payload, ok := c.pending[key]
	if !ok {
		return nil, nil
	}
	return payload, nil
}

func (c *seedanceTestCache) SetGrokVideoPendingBilling(_ context.Context, key string, payload []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[key] = append([]byte(nil), payload...)
	return nil
}

func (c *seedanceTestCache) GetSessionAccountID(_ context.Context, groupID int64, sessionHash string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[fmt.Sprintf("%d:%s", groupID, sessionHash)], nil
}

func (c *seedanceTestCache) SetSessionAccountID(_ context.Context, groupID int64, sessionHash string, accountID int64, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[fmt.Sprintf("%d:%s", groupID, sessionHash)] = accountID
	return nil
}

func newSeedanceOwnershipHandler(t *testing.T, cache *seedanceTestCache, account *service.Account) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	repo := &grokGatewayLiteE2EAccountRepo{account: *account}
	svc := newSeedanceTestGatewayService(repo, cache, cfg)
	return &OpenAIGatewayHandler{gatewayService: svc}
}

// newSeedanceTestGatewayService 用最小依赖装配网关服务，只对归属链路需要的缓存与账号仓库注入替身。
func newSeedanceTestGatewayService(repo service.AccountRepository, cache service.GatewayCache, cfg *config.Config) *service.OpenAIGatewayService {
	return service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, cache, cfg,
		nil, nil, service.NewBillingService(cfg, nil), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func TestSeedanceCreationRejectsUnavailablePricingCandidates(t *testing.T) {
	cache := newSeedanceTestCache()
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{"customer-video-no-price": "mapped-video-no-price"}}}
	h := newSeedanceOwnershipHandler(t, cache, account)
	key := &service.APIKey{ID: 11, UserID: 33}
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "no-price-create")
	_, err := h.gatewayService.PrepareSeedanceTaskCreation(ctx, key, 33, account, "customer-video-no-price", "customer-video-no-price")
	require.ErrorIs(t, err, service.ErrModelPricingUnavailable)
	require.Empty(t, cache.pending, "所有候选无价时不得写入可执行任务预登记")
}

func seedanceTestRequest(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/contents/generations/tasks/task-1", nil)
	return c
}

// TestSeedanceTaskOwnershipRequiresCreateTimeRecord 钉住 Seedance 的 fail-closed 归属语义：
// 没有创建时写入的待结算快照（或没有账号绑定）时，查询/删除必须按「不存在」处理，
// 绝不能拿上游 task_id 当跨用户凭证去查别人的任务。
func TestSeedanceTaskOwnershipRequiresCreateTimeRecord(t *testing.T) {
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	groupID := int64(7)
	apiKey := &service.APIKey{ID: 11, GroupID: &groupID}
	subject := middleware2.AuthSubject{UserID: 33}
	requestID := service.SeedanceTaskKey("task-1")

	// 1) 缓存里什么都没有：不存在
	cache := newSeedanceTestCache()
	h := newSeedanceOwnershipHandler(t, cache, account)
	_, err := h.resolveSeedanceTaskAccount(seedanceTestRequest(t), apiKey, subject, requestID)
	require.ErrorIs(t, err, ErrSeedanceTaskNotFound)

	// 2) 只有快照、没有账号绑定：仍然不存在（避免重新调度到别的账号）
	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(
		context.Background(), requestID, subject.UserID, apiKey.ID,
		service.GrokVideoPendingBilling{Model: "seedance-video"},
	))
	_, err = h.resolveSeedanceTaskAccount(seedanceTestRequest(t), apiKey, subject, requestID)
	require.ErrorIs(t, err, ErrSeedanceTaskNotFound)

	// 3) 快照 + 绑定齐全：返回创建时的账号
	require.NoError(t, h.gatewayService.BindSeedanceTaskAccount(context.Background(), apiKey.GroupID, requestID, account.ID))
	resolved, err := h.resolveSeedanceTaskAccount(seedanceTestRequest(t), apiKey, subject, requestID)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.EqualValues(t, account.ID, resolved.ID)

	// 4) 换一个 API Key（同一用户）查询：快照键含 KeyID，必须按不存在处理
	otherKey := &service.APIKey{ID: 12, GroupID: &groupID}
	_, err = h.resolveSeedanceTaskAccount(seedanceTestRequest(t), otherKey, subject, requestID)
	require.ErrorIs(t, err, ErrSeedanceTaskNotFound)

	// 5) 换一个用户：同样不存在
	_, err = h.resolveSeedanceTaskAccount(seedanceTestRequest(t), apiKey, middleware2.AuthSubject{UserID: 44}, requestID)
	require.ErrorIs(t, err, ErrSeedanceTaskNotFound)
}

// TestSeedancePendingBillingRoundTrip 覆盖本轮补上的写入侧：
// 以前仓库只有读取方，成功任务永远拿不到创建时口径，导致不入账。
func TestSeedancePendingBillingRoundTrip(t *testing.T) {
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	cache := newSeedanceTestCache()
	h := newSeedanceOwnershipHandler(t, cache, account)
	requestID := service.SeedanceTaskKey("task-2")

	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(
		context.Background(), requestID, 33, 11,
		service.GrokVideoPendingBilling{Model: "  seedance-video  ", BillingModel: "", UpstreamModel: "doubao-seedance-1-0-pro"},
	))

	pending, err := h.gatewayService.LoadGrokVideoPendingBilling(context.Background(), requestID, 33, 11)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, "seedance-video", pending.Model)
	require.Equal(t, "doubao-seedance-1-0-pro", pending.UpstreamModel)
	require.NotEmpty(t, pending.CreatedAt, "创建受理时刻必须被补上，用于端到端时长")

	// 归属键含用户与 API Key：其他用户/Key 读不到
	missing, err := h.gatewayService.LoadGrokVideoPendingBilling(context.Background(), requestID, 99, 11)
	require.NoError(t, err)
	require.Nil(t, missing)
}

// TestSeedanceCacheUnavailableIsNotTreatedAsMissing 明确错误与「不存在」的区别：
// 缓存不可用时返回错误（handler 会回 503），而不是伪装成 404。
func TestSeedanceCacheUnavailableIsNotTreatedAsMissing(t *testing.T) {
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	repo := &grokGatewayLiteE2EAccountRepo{account: *account}
	svc := newSeedanceTestGatewayService(repo, nil, cfg)
	h := &OpenAIGatewayHandler{gatewayService: svc}
	groupID := int64(7)
	apiKey := &service.APIKey{ID: 11, GroupID: &groupID}

	_, err := h.resolveSeedanceTaskAccount(seedanceTestRequest(t), apiKey, middleware2.AuthSubject{UserID: 33}, service.SeedanceTaskKey("task-3"))
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrSeedanceTaskNotFound))
}

func TestSeedanceRemoteIdentityIsolationAndOriginalAccount(t *testing.T) {
	groupID := int64(7)
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	cache := newSeedanceTestCache()
	h := newSeedanceOwnershipHandler(t, cache, account)
	key := &service.APIKey{ID: 0, UserID: 33, GroupID: &groupID}
	meta := gatewaylite.RequestMeta{RequestID: "create-r1", KeyID: "remote-key", UserID: 33, GatewayID: "gateway-a", GroupID: groupID,
		OrganizationID: 9, BillingSubjectType: "organization", BillingSubjectID: 9, LeaseID: "original-lease"}
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	owner, err := service.ResolveMediaTaskIdentity(ctx, key, 33)
	require.NoError(t, err)
	taskID := service.SeedanceTaskKey("remote-task")
	require.NoError(t, h.gatewayService.StoreSeedancePendingBilling(ctx, taskID, owner, service.GrokVideoPendingBilling{Model: "video", AccountID: account.ID, GroupID: groupID}))
	c := seedanceTestRequest(t)
	c.Request = c.Request.WithContext(ctx)
	resolved, err := h.resolveSeedanceTaskAccount(c, key, middleware2.AuthSubject{UserID: 33}, taskID)
	require.NoError(t, err)
	require.Equal(t, account.ID, resolved.ID, "任务快照直接保存原账号，不依赖调度粘性 TTL")

	for _, scope := range []string{"key", "user", "organization", "payer", "gateway", "local"} {
		t.Run(scope, func(t *testing.T) {
			other, caller, otherKey := meta, int64(33), *key
			switch scope {
			case "key":
				other.KeyID = "different-key"
			case "user":
				other.UserID, otherKey.UserID, caller = 44, 44, 44
			case "organization":
				other.OrganizationID = 10
			case "payer":
				other.BillingSubjectType, other.BillingSubjectID = "user", 33
			case "gateway":
				other.GatewayID = "gateway-b"
			case "local":
				otherKey.ID = 1
			}
			otherCtx := gatewaylite.ContextWithRequestMeta(context.Background(), other)
			if scope == "local" {
				otherCtx = context.Background()
			}
			c := seedanceTestRequest(t)
			c.Request = c.Request.WithContext(otherCtx)
			_, err := h.resolveSeedanceTaskAccount(c, &otherKey, middleware2.AuthSubject{UserID: caller}, taskID)
			require.ErrorIs(t, err, ErrSeedanceTaskNotFound)
		})
	}
}

func seedanceTestExecutionPricing(t *testing.T, model string, rate float64) *service.ExecutionPricingSnapshot {
	t.Helper()
	snapshot, err := service.FreezeExecutionPricing(service.CostInput{Model: model, RateMultiplier: rate,
		Resolved: &service.ResolvedPricing{Mode: service.BillingModeToken, Source: service.PricingSourceChannel,
			BasePricing: &service.ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 30e-6}}})
	require.NoError(t, err)
	return snapshot
}

func TestSeedanceCompletionRestoresFundingAndRetriesFailedRecord(t *testing.T) {
	groupID := int64(7)
	cache := newSeedanceTestCache()
	h := newSeedanceOwnershipHandler(t, cache, &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	key := &service.APIKey{ID: 0, UserID: 33, GroupID: &groupID}
	meta := gatewaylite.RequestMeta{RequestID: "original-create", KeyID: "remote-key", UserID: 33, GatewayID: "gateway-a", GroupID: groupID, LeaseID: "original-lease",
		BillingSubjectType: "user", BillingSubjectID: 33, EntryGlobalGroupID: 5, EntryGatewayNodeID: 6}
	auth := gatewaylite.FundingAuthorization{AuthorizationID: "original-funding", RequestID: meta.RequestID, KeyID: meta.KeyID, UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33, Version: 3,
		Items: []gatewaylite.FundingAuthorizationItem{{ItemID: 7, Source: "time_card", ReservedMicroCents: 900}}}
	meta.Funding = gatewaylite.NewRequestFunding(auth, nil, nil)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	policy, err := service.ResolveEffectiveRate(&service.Group{ID: groupID, RateMultiplier: 2}, nil, time.Now())
	require.NoError(t, err)
	ctx = service.ContextWithEffectiveRatePolicy(ctx, policy)
	owner, err := service.ResolveMediaTaskIdentity(ctx, key, 33)
	require.NoError(t, err)
	taskID := service.SeedanceTaskKey("billable-task")
	require.NoError(t, h.gatewayService.StoreSeedancePendingBilling(ctx, taskID, owner, service.GrokVideoPendingBilling{AccountID: 4242, GroupID: groupID, Model: "video", ExecutionPricing: seedanceTestExecutionPricing(t, "video", 2), CreatedAt: time.Now().Add(-time.Minute).Format(time.RFC3339Nano)}))
	pending, err := h.gatewayService.LoadSeedancePendingBilling(ctx, taskID, owner)
	require.NoError(t, err)
	require.Nil(t, pending.RequestMeta.Funding, "进程内锁不能序列化为资金授权")
	require.Equal(t, auth, *pending.FundingSnapshot)

	pollMeta := meta
	pollMeta.RequestID, pollMeta.LeaseID, pollMeta.Funding = "free-poll", "poll-lease", nil
	pollCtx := gatewaylite.ContextWithRequestMeta(context.Background(), pollMeta)
	subject := middleware2.AuthSubject{UserID: 33}
	result := &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 12345}, ResponseID: taskID}
	completion, err := prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.NotNil(t, completion)
	attempts := 0
	err = settleSeedanceCompletion(pollCtx, h, completion, func(recordCtx context.Context, billed *service.OpenAIForwardResult) error {
		attempts++
		restored, ok := gatewaylite.RequestMetaFromContext(recordCtx)
		require.True(t, ok)
		require.Equal(t, "original-create", restored.RequestID)
		require.Equal(t, "original-lease", restored.LeaseID)
		require.True(t, restored.AsyncSettlement)
		require.EqualValues(t, 5, restored.EntryGlobalGroupID)
		funding, ok := gatewaylite.FundingFromContext(recordCtx)
		require.True(t, ok)
		require.Equal(t, auth, funding)
		rate, ok := service.EffectiveRatePolicyFromContext(recordCtx)
		require.True(t, ok)
		require.Equal(t, policy, rate)
		require.Equal(t, 12345, billed.Usage.OutputTokens)
		require.Zero(t, billed.VideoCount)
		return errors.New("reliable outbox unavailable")
	})
	require.Error(t, err)
	completion, err = prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.NotNil(t, completion, "失败后认领释放，原任务可重试")
	err = settleSeedanceCompletion(pollCtx, h, completion, func(context.Context, *service.OpenAIForwardResult) error { attempts++; return nil })
	require.NoError(t, err)
	for range 20 {
		completion, err = prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
		require.NoError(t, err)
		require.Nil(t, completion)
	}
	require.Equal(t, 2, attempts)
}

func TestSeedanceCompletionUnknownRecordKeepsClaimAndReplaysOriginalIdentity(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	gateway := newSeedanceTestGatewayService(nil, repository.NewGatewayCache(client), &config.Config{RunMode: config.RunModeGatewayLite})
	h := &OpenAIGatewayHandler{gatewayService: gateway}
	key := &service.APIKey{ID: 0, UserID: 33}
	meta := gatewaylite.RequestMeta{RequestID: "original-unknown-create", KeyID: "remote-key", UserID: 33, GatewayID: "gateway-a", LeaseID: "original-lease"}
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	owner, err := service.ResolveMediaTaskIdentity(ctx, key, 33)
	require.NoError(t, err)
	taskID := service.SeedanceTaskKey("unknown-billing-task")
	require.NoError(t, gateway.StoreSeedancePendingBilling(ctx, taskID, owner, service.GrokVideoPendingBilling{Model: "video", AccountID: 4242, ExecutionPricing: seedanceTestExecutionPricing(t, "video", 1)}))
	meta.RequestID, meta.LeaseID = "free-poll", "poll-lease"
	pollCtx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	result := &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 999}, ResponseID: taskID}
	subject := middleware2.AuthSubject{UserID: 33}
	first, err := prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.ErrorIs(t, settleSeedanceCompletion(pollCtx, h, first, func(context.Context, *service.OpenAIForwardResult) error {
		return errors.Join(errSeedanceBillingPersistenceUncertain, context.DeadlineExceeded)
	}), errSeedanceBillingPersistenceUncertain)
	blocked, err := prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.Nil(t, blocked, "未知提交保留认领，不立即重复结算")
	mini.FastForward(2*time.Minute + time.Millisecond)
	replay, err := prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.NotNil(t, replay, "认领过期后恢复，而非永久漏收")
	require.NotEqual(t, first.token, replay.token)
	require.NoError(t, gateway.ReleaseSeedanceBilling(pollCtx, taskID, owner, first.token))
	require.Error(t, gateway.ConfirmSeedanceBilling(pollCtx, taskID, owner, first.token))
	stillBlocked, err := prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.Nil(t, stillBlocked, "旧 worker 不能释放或确认已接管的认领")
	require.NoError(t, settleSeedanceCompletion(pollCtx, h, replay, func(recordCtx context.Context, billed *service.OpenAIForwardResult) error {
		restored, ok := gatewaylite.RequestMetaFromContext(recordCtx)
		require.True(t, ok)
		require.Equal(t, "original-unknown-create", restored.RequestID)
		require.Equal(t, "original-lease", restored.LeaseID)
		require.Equal(t, first.result.RequestID, billed.RequestID, "重放保持稳定计费编号")
		return nil
	}))
	done, err := prepareSeedanceCompletionBilling(pollCtx, h, key, subject, taskID, result)
	require.NoError(t, err)
	require.Nil(t, done)
}

func TestSeedanceCompletionRejectsMissingAndDamagedExecutionPriceBeforeClaim(t *testing.T) {
	cache := newSeedanceTestCache()
	h := newSeedanceOwnershipHandler(t, cache, &service.Account{ID: 4242})
	key := &service.APIKey{ID: 11, UserID: 33}
	owner, err := service.ResolveMediaTaskIdentity(context.Background(), key, 33)
	require.NoError(t, err)
	taskID := service.SeedanceTaskKey("price-required")
	pending := service.GrokVideoPendingBilling{Model: "video", AccountID: 4242}
	require.NoError(t, h.gatewayService.StoreSeedancePendingBilling(context.Background(), taskID, owner, pending))
	result := &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 1}}
	completion, err := prepareSeedanceCompletionBilling(context.Background(), h, key, middleware2.AuthSubject{UserID: 33}, taskID, result)
	require.ErrorContains(t, err, "execution pricing")
	require.Nil(t, completion)
	require.Empty(t, cache.claims, "缺失原价不能先占认领或按当前价记账")
	pending.ExecutionPricing = seedanceTestExecutionPricing(t, "video", 1)
	pending.ExecutionPricing.Resolved.BasePricing.OutputPricePerToken = -1
	require.Error(t, h.gatewayService.StoreSeedancePendingBilling(context.Background(), taskID, owner, pending))
}

func TestSeedanceLocalCompletionDoesNotUsePollRequestID(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "free-poll")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "poll-client-id")
	pending := &service.GrokVideoPendingBilling{TaskID: service.SeedanceTaskKey("local-task")}
	restored := service.SeedanceCompletionBillingContext(ctx, pending)
	id, ok := gatewaylite.CanonicalRequestIDFromContext(restored)
	require.True(t, ok)
	require.Equal(t, "local:"+service.StableGrokVideoBillingRequestID(pending.TaskID), id)
}
